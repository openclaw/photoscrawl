package place

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	backfillRadiusMeters = defaultRadiusMeters
	backfillMaxAttempts  = 4
	backfillWorkers      = 4
	backfillStartEvery   = 1500 * time.Millisecond
	backfillCooldown     = 2 * time.Minute
)

type BackfillOptions struct {
	DatabasePath string
	OutputDir    string
}

type BackfillResult struct {
	DatabasePath       string    `json:"database_path"`
	OutputDir          string    `json:"output_dir"`
	ManifestPath       string    `json:"manifest_path"`
	StartedAt          time.Time `json:"started_at"`
	FinishedAt         time.Time `json:"finished_at"`
	LocatedAssets      int       `json:"located_assets"`
	LocationKeys       int       `json:"location_keys"`
	ExistingSuccesses  int       `json:"existing_successes"`
	Successes          int       `json:"successes"`
	FinalFailures      int       `json:"final_failures"`
	ProviderAttempts   int       `json:"provider_attempts"`
	AttemptFailures    int       `json:"attempt_failures"`
	RadiusMeters       float64   `json:"radius_meters"`
	MaxAttemptsPerKey  int       `json:"max_attempts_per_key"`
	Workers            int       `json:"workers"`
	ProviderStartEvery string    `json:"provider_start_every"`
	ProviderCooldowns  int       `json:"provider_cooldowns"`
}

type backfillKey struct {
	Index                 int     `json:"index"`
	Latitude              float64 `json:"latitude"`
	Longitude             float64 `json:"longitude"`
	AccuracyMeters        float64 `json:"accuracy_meters,omitempty"`
	AssetCount            int     `json:"asset_count"`
	RepresentativeAssetID string  `json:"representative_asset_id"`
	FirstSeen             string  `json:"first_seen,omitempty"`
	LastSeen              string  `json:"last_seen,omitempty"`
	key                   string
}

type backfillError struct {
	Index          int       `json:"index"`
	Attempts       int       `json:"attempts"`
	Final          bool      `json:"final"`
	Error          string    `json:"error"`
	LastAttemptAt  time.Time `json:"last_attempt_at"`
	NextRetryRound int       `json:"next_retry_round,omitempty"`
}

type backfillAttempt struct {
	Index     int       `json:"index"`
	Attempt   int       `json:"attempt"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Error     string    `json:"error,omitempty"`
	Success   bool      `json:"success"`
}

func Backfill(ctx context.Context, opts BackfillOptions) (BackfillResult, error) {
	if strings.TrimSpace(opts.DatabasePath) == "" {
		return BackfillResult{}, errors.New("database path is required")
	}
	if strings.TrimSpace(opts.OutputDir) == "" {
		return BackfillResult{}, errors.New("output dir is required")
	}

	startedAt := time.Now().UTC()
	keys, locatedAssets, err := loadBackfillKeys(ctx, opts.DatabasePath)
	if err != nil {
		return BackfillResult{}, err
	}
	if err := ensureBackfillDirs(opts.OutputDir); err != nil {
		return BackfillResult{}, err
	}
	manifestPath := filepath.Join(opts.OutputDir, "manifest.json")
	if err := writeJSONFile(manifestPath, keys); err != nil {
		return BackfillResult{}, err
	}

	state := &backfillRunState{
		startedAt:    startedAt,
		outputDir:    opts.OutputDir,
		progressPath: filepath.Join(opts.OutputDir, "logs", "progress.tsv"),
		result: BackfillResult{
			DatabasePath:       opts.DatabasePath,
			OutputDir:          opts.OutputDir,
			ManifestPath:       manifestPath,
			StartedAt:          startedAt,
			LocatedAssets:      locatedAssets,
			LocationKeys:       len(keys),
			RadiusMeters:       backfillRadiusMeters,
			MaxAttemptsPerKey:  backfillMaxAttempts,
			Workers:            backfillWorkers,
			ProviderStartEvery: backfillStartEvery.String(),
		},
	}
	state.result.ExistingSuccesses = countMatchingOutputs(opts.OutputDir, keys)
	state.result.Successes = state.result.ExistingSuccesses

	for attempt := 1; attempt <= backfillMaxAttempts; attempt++ {
		jobs, err := backfillJobs(opts.OutputDir, keys, attempt)
		if err != nil {
			return state.result, err
		}
		if len(jobs) == 0 {
			continue
		}
		if attempt > 1 {
			if err := sleepContext(ctx, backfillRetryDelay(attempt)); err != nil {
				return state.result, err
			}
		}
		if err := runBackfillRound(ctx, jobs, attempt, state); err != nil {
			return state.result, err
		}
	}

	state.result.Successes = countSuccessOutputs(opts.OutputDir)
	state.result.FinalFailures = countFinalErrors(opts.OutputDir)
	state.result.FinishedAt = time.Now().UTC()
	if err := writeJSONFile(filepath.Join(opts.OutputDir, "summary.json"), state.result); err != nil {
		return state.result, err
	}
	return state.result, nil
}

type backfillRunState struct {
	mu           sync.Mutex
	startedAt    time.Time
	outputDir    string
	progressPath string
	result       BackfillResult
	limiter      *backfillLimiter
}

func runBackfillRound(ctx context.Context, jobs []backfillKey, attempt int, state *backfillRunState) error {
	work := make(chan backfillKey)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	state.limiter = &backfillLimiter{interval: backfillStartEvery}

	for i := 0; i < backfillWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range work {
				if err := ctx.Err(); err != nil {
					setFirstErr(&errMu, &firstErr, err)
					return
				}
				if err := state.limiter.wait(ctx); err != nil {
					setFirstErr(&errMu, &firstErr, err)
					return
				}
				if err := attemptBackfillKey(ctx, key, attempt, state); err != nil {
					setFirstErr(&errMu, &firstErr, err)
					return
				}
			}
		}()
	}
	for _, key := range jobs {
		select {
		case <-ctx.Done():
			close(work)
			wg.Wait()
			if firstErr != nil {
				return firstErr
			}
			return ctx.Err()
		case work <- key:
		}
	}
	close(work)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

type backfillLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func (limiter *backfillLimiter) wait(ctx context.Context) error {
	limiter.mu.Lock()
	now := time.Now()
	if limiter.next.IsZero() || now.After(limiter.next) {
		limiter.next = now
	}
	startAt := limiter.next
	limiter.next = startAt.Add(limiter.interval)
	limiter.mu.Unlock()

	return sleepContext(ctx, time.Until(startAt))
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (limiter *backfillLimiter) cooldown() {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	next := time.Now().Add(backfillCooldown)
	if limiter.next.Before(next) {
		limiter.next = next
	}
}

func attemptBackfillKey(ctx context.Context, key backfillKey, attempt int, state *backfillRunState) error {
	input := Input{
		AssetID: key.RepresentativeAssetID,
		TakenAt: key.LastSeen,
		Location: Coordinate{
			Latitude:  key.Latitude,
			Longitude: key.Longitude,
		},
		AccuracyMeters: key.AccuracyMeters,
	}
	startedAt := time.Now().UTC()
	result, err := rawAppleResultSubprocess(ctx, input)
	endedAt := time.Now().UTC()
	attemptRecord := backfillAttempt{
		Index:     key.Index,
		Attempt:   attempt,
		StartedAt: startedAt,
		EndedAt:   endedAt,
		Success:   err == nil,
	}
	if err != nil {
		attemptRecord.Error = err.Error()
		if appendErr := appendAttempt(filepath.Join(state.outputDir, "attempts", key.filename()+".jsonl"), attemptRecord); appendErr != nil {
			return appendErr
		}
		final := attempt >= backfillMaxAttempts
		failure := backfillError{
			Index:         key.Index,
			Attempts:      attempt,
			Final:         final,
			Error:         err.Error(),
			LastAttemptAt: endedAt,
		}
		if !final {
			failure.NextRetryRound = attempt + 1
		}
		if err := writeJSONFile(filepath.Join(state.outputDir, "errors", key.filename()+".json"), failure); err != nil {
			return err
		}
		if isReverseGeocodeThrottle(err) {
			state.recordCooldown()
		}
		state.recordAttempt(false)
		return nil
	}
	if err := appendAttempt(filepath.Join(state.outputDir, "attempts", key.filename()+".jsonl"), attemptRecord); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(state.outputDir, "outputs", key.filename()+".json"), result); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(state.outputDir, "errors", key.filename()+".json"))
	state.recordAttempt(true)
	return nil
}

func rawAppleResultSubprocess(ctx context.Context, input Input) (Result, error) {
	executable, err := os.Executable()
	if err != nil {
		return Result{}, err
	}
	data, err := json.Marshal(input)
	if err != nil {
		return Result{}, err
	}
	cmd := exec.CommandContext(ctx, executable, "place-context-raw", "--input", "-")
	cmd.Stdin = bytes.NewReader(data)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return Result{}, errors.New(message)
	}
	var result Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return Result{}, fmt.Errorf("decode raw Apple result: %w", err)
	}
	return result, nil
}

func (state *backfillRunState) recordAttempt(success bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.result.ProviderAttempts++
	if success {
		state.result.Successes++
	} else {
		state.result.AttemptFailures++
	}
	if state.result.ProviderAttempts%100 != 0 {
		return
	}
	_ = appendProgress(state.progressPath, state.startedAt, state.result)
}

func (state *backfillRunState) recordCooldown() {
	if state.limiter != nil {
		state.limiter.cooldown()
	}
	state.mu.Lock()
	state.result.ProviderCooldowns++
	state.mu.Unlock()
}

func isReverseGeocodeThrottle(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "Apple reverse geocode failed") ||
		strings.Contains(message, "Apple reverse geocode timed out")
}

func setFirstErr(mu *sync.Mutex, target *error, err error) {
	mu.Lock()
	defer mu.Unlock()
	if *target == nil {
		*target = err
	}
}
