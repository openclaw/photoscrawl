"""CI-only frozen Go advisory staging and structured scan verification."""

import argparse
import concurrent.futures
import datetime
import gzip
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request


def json_stream(text):
    decoder = json.JSONDecoder()
    offset = 0
    while offset < len(text):
        while offset < len(text) and text[offset].isspace():
            offset += 1
        if offset == len(text):
            break
        value, offset = decoder.raw_decode(text, offset)
        if not isinstance(value, dict):
            raise ValueError("expected a JSON object")
        yield value


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def command(*args):
    return subprocess.check_output(args, text=True, timeout=120)


def public_bytes(path):
    for attempt in range(3):
        try:
            with urllib.request.urlopen("https://vuln.go.dev/" + path, timeout=30) as response:
                return response.read()
        except (urllib.error.URLError, TimeoutError, ConnectionError, http.client.IncompleteRead) as error:
            if isinstance(error, urllib.error.HTTPError):
                error.close()
                if error.code not in (408, 429) and not 500 <= error.code < 600:
                    raise
            if attempt == 2:
                raise
            time.sleep(2 ** attempt)


def graph():
    text = command("go", "list", "-m", "-json", "all")
    modules = list(json_stream(text))
    if not modules or any(module.get("Replace") for module in modules):
        raise ValueError("missing or replaced selected module graph")
    return text, {module["Path"] for module in modules} | {"stdlib", "toolchain"}


def required_records(index, paths):
    records = {}
    for module in index:
        if module["path"] in paths:
            for record in module["vulns"]:
                if not re.fullmatch(r"GO-\d{4}-\d+", record["id"]):
                    raise ValueError("invalid advisory record ID")
                records[record["id"]] = record["modified"]
    return records


def file_hashes(directory):
    return {
        str(path.relative_to(directory)): hashlib.sha256(path.read_bytes()).hexdigest()
        for path in sorted(directory.rglob("*.json"))
        if path.name != "frozen.json"
    }


def stage(directory):
    directory.mkdir(parents=True, exist_ok=False)
    (directory / "index").mkdir()
    (directory / "ID").mkdir()
    _, paths = graph()
    for name in ("db", "modules"):
        data = gzip.decompress(public_bytes("index/" + name + ".json.gz"))
        json.loads(data)
        (directory / "index" / (name + ".json")).write_bytes(data)
    index = json.loads((directory / "index/modules.json").read_text())
    records = required_records(index, paths)

    def fetch(item):
        identifier, modified = item
        data = public_bytes("ID/" + identifier + ".json")
        record = json.loads(data)
        if record["id"] != identifier or record["modified"] != modified:
            raise ValueError("advisory changed while staging: " + identifier)
        (directory / "ID" / (identifier + ".json")).write_bytes(data)

    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        list(pool.map(fetch, sorted(records.items())))
    database = json.loads((directory / "index/db.json").read_text())
    if json.loads(gzip.decompress(public_bytes("index/db.json.gz"))) != database:
        raise ValueError("advisory index changed while staging")
    write_json(directory / "frozen.json", {
        "retrieved_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "source": "https://vuln.go.dev",
        "database": database,
        "module_paths": sorted(paths),
        "records": sorted(records),
        "files": file_hashes(directory),
    })


def expected_sbom(text, selected, version):
    selected_versions = {module["Path"]: module.get("Version", "") for module in selected}
    roots = set()
    modules = {"stdlib": "v" + version.removeprefix("go")}
    for package in json_stream(text):
        if package.get("Error") or package.get("DepsErrors") or package.get("Incomplete"):
            raise ValueError("package graph did not load completely")
        if not package.get("DepOnly"):
            roots.add(package["ImportPath"])
        module = package.get("Module")
        if module is None:
            continue
        path, module_version = module["Path"], module.get("Version", "")
        if module.get("Replace") or module.get("Error") or selected_versions.get(path) != module_version:
            raise ValueError("loaded package module differs from selected graph")
        modules[path] = module_version
    if not roots:
        raise ValueError("no packages match the scan pattern")
    return {"roots": sorted(roots), "modules": modules}


def evaluate(text, expected, packages):
    messages = list(json_stream(text))
    if not messages or set(messages[0]) != {"config"}:
        raise ValueError("missing initial scanner Config")
    if sum("config" in message for message in messages) != 1:
        raise ValueError("duplicate scanner Config")
    config = messages[0]["config"]
    for key, value in expected.items():
        if config.get(key) != value:
            raise ValueError("scanner Config mismatch: " + key)
    levels = {}
    candidates = set()
    sboms = []
    for message in messages:
        if len(message) != 1 or not set(message) <= {"config", "progress", "SBOM", "osv", "finding"}:
            raise ValueError("unexpected scanner message")
        if "SBOM" in message:
            sboms.append(message["SBOM"])
        if "osv" in message:
            candidates.add(message["osv"]["id"])
        if "finding" not in message:
            continue
        finding = message["finding"]
        trace = finding.get("trace")
        if not finding.get("osv") or not trace or not trace[0].get("module"):
            raise ValueError("incomplete finding")
        frame = trace[0]
        level = 3 if frame.get("function") else 2 if frame.get("package") else 1
        levels[finding["osv"]] = max(levels.get(finding["osv"], 0), level)
    if len(sboms) != 1 or sboms[0].get("go_version") != expected["go_version"]:
        raise ValueError("missing or mismatched scanner SBOM")
    roots = sboms[0].get("roots", [])
    if not roots or len(set(roots)) != len(roots) or set(roots) != set(packages["roots"]):
        raise ValueError("scanner roots differ from loaded packages")
    scanned_modules = {}
    for module in sboms[0].get("modules", []):
        path = module.get("path")
        if not path or path in scanned_modules:
            raise ValueError("missing or duplicate scanner module")
        scanned_modules[path] = module.get("version", "")
    if scanned_modules != packages["modules"]:
        raise ValueError("scanner modules differ from loaded package graph")
    if not set(levels) <= candidates:
        raise ValueError("finding without its advisory evidence")
    return {
        "candidate_advisories": len(candidates),
        "called": sorted(key for key, level in levels.items() if level == 3),
        "imported_only": sorted(key for key, level in levels.items() if level == 2),
        "module_only": sorted(key for key, level in levels.items() if level == 1),
        "findings_require_review": bool(levels),
    }


def validate_environment(environment, goenv):
    # `go env GOENV` reports the resolved filename, empty when GOENV=off.
    if goenv != "off" or environment.get("GOENV") != "":
        raise ValueError("Go environment file is not disabled")
    if any(environment.get(key) != value for key, value in {
        "GOTOOLCHAIN": "local", "GOWORK": "off",
    }.items()) or "tags" in environment["GOFLAGS"]:
        raise ValueError("unexpected toolchain/workspace/build-tag configuration")


def scan(database, scanner, output, version, platform):
    output.mkdir(parents=True, exist_ok=False)
    frozen = json.loads((database / "frozen.json").read_text())
    if file_hashes(database) != frozen["files"]:
        raise ValueError("frozen advisory bytes changed")
    selected, paths = graph()
    required = required_records(json.loads((database / "index/modules.json").read_text()), paths)
    if not set(required) <= set(frozen["records"]):
        raise ValueError("frozen database does not cover the selected graph")
    (output / "modules.json").write_text(selected)
    (output / "module-graph.txt").write_text(command("go", "mod", "graph"))
    environment = json.loads(command(
        "go", "env", "-json", "GOVERSION", "GOOS", "GOARCH", "CGO_ENABLED",
        "GOFLAGS", "GOTOOLCHAIN", "GOWORK", "GOENV"))
    if environment["GOVERSION"] != version or environment["GOOS"] != platform:
        raise ValueError("actual compiler or platform mismatch")
    validate_environment(environment, os.environ.get("GOENV"))
    loaded = command("go", "list", "-deps", "-json", "./...")
    (output / "loaded-packages.json").write_text(loaded)
    packages = expected_sbom(loaded, list(json_stream(selected)), version)
    write_json(output / "expected-sbom.json", packages)
    db_uri = database.resolve().as_uri()
    argv = [str(scanner), "-db=" + db_uri, "-json", "./..."]
    write_json(output / "invocation.json", {
        "source": command("git", "rev-parse", "HEAD").strip(),
        "environment": environment,
        "requested_goenv": os.environ["GOENV"],
        "build_tags": [],
        "scanner_sha256": hashlib.sha256(scanner.read_bytes()).hexdigest(),
        "database_manifest_sha256": hashlib.sha256((database / "frozen.json").read_bytes()).hexdigest(),
        "argv": argv,
    })
    (output / "scanner-build.txt").write_text(command("go", "version", "-m", str(scanner)))
    completed = False
    exit_code = None
    try:
        with (output / "scan.json").open("w") as stdout, (output / "scan.stderr").open("w") as stderr:
            result = subprocess.run(
                argv, stdout=stdout, stderr=stderr, timeout=600, check=False,
                env={**os.environ, "GOPROXY": "off", "GOSUMDB": "off"})
            completed, exit_code = True, result.returncode
    finally:
        write_json(output / "completion.json", {"completed": completed, "exit_code": exit_code})
    if not completed or exit_code != 0:
        raise ValueError("scanner failed or did not complete")
    if file_hashes(database) != frozen["files"]:
        raise ValueError("frozen advisory bytes changed during scan")
    summary = evaluate((output / "scan.json").read_text(), {
        "protocol_version": "v1.0.0", "scanner_name": "govulncheck",
        "scanner_version": "v1.8.0", "db": db_uri,
        "db_last_modified": frozen["database"]["modified"],
        "go_version": version, "scan_level": "symbol", "scan_mode": "source",
    }, packages)
    write_json(output / "summary.json", summary)
    print(json.dumps(summary, sort_keys=True))
    if summary["findings_require_review"]:
        raise ValueError("advisory findings require an explicit disposition")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    prepare = commands.add_parser("stage")
    prepare.add_argument("database", type=Path)
    run = commands.add_parser("scan")
    run.add_argument("database", type=Path)
    run.add_argument("scanner", type=Path)
    run.add_argument("output", type=Path)
    run.add_argument("version")
    run.add_argument("platform", choices=["darwin", "linux"])
    args = parser.parse_args()
    if args.command == "stage":
        stage(args.database)
    else:
        scan(args.database, args.scanner, args.output, args.version, args.platform)


if __name__ == "__main__":
    os.umask(0o077)
    try:
        main()
    except Exception as error:
        print("vulnerability proof failed: " + str(error), file=sys.stderr)
        sys.exit(1)
