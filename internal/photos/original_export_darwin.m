#import <Foundation/Foundation.h>
#import <Photos/Photos.h>
#import <dispatch/dispatch.h>
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include "original_export_darwin.h"

@interface PCOriginalExport : NSObject {
@public
  dispatch_semaphore_t completed;
  BOOL finished;
  BOOL cancelled;
  int descriptor;
  PHAssetResourceDataRequestID requestID;
  NSString *stagingPath;
  NSString *destinationPath;
  NSString *errorDescription;
}
- (BOOL)prepare:(NSString *)path;
- (void)cancel;
- (void)registerRequest:(PHAssetResourceDataRequestID)value;
- (void)receive:(NSData *)data;
- (void)complete:(NSError *)error;
@end

@implementation PCOriginalExport
- (id)init {
  if ((self = [super init])) {
    descriptor = -1;
    completed = dispatch_semaphore_create(0);
  }
  return self;
}
- (void)dealloc {
  [stagingPath release];
  [destinationPath release];
  [errorDescription release];
  dispatch_release(completed);
  [super dealloc];
}
// The caller holds the monitor for every transition and filesystem operation.
- (void)failLocked:(NSString *)message {
  if (finished) return;
  errorDescription = [message copy];
  if (descriptor >= 0) {
    close(descriptor);
    descriptor = -1;
  }
  if (stagingPath != nil) unlink(stagingPath.fileSystemRepresentation);
  finished = YES;
  dispatch_semaphore_signal(completed);
}
- (void)cancelRequest {
  PHAssetResourceDataRequestID value;
  @synchronized(self) { value = requestID; }
  // PhotoKit may invoke a callback synchronously while cancelling.
  if (value != PHInvalidAssetResourceDataRequestID) {
    [[PHAssetResourceManager defaultManager] cancelDataRequest:value];
  }
}
- (void)cancel {
  @synchronized(self) {
    if (finished) return;
    cancelled = YES;
    [self failLocked:@"export original resource canceled"];
  }
  [self cancelRequest];
}
- (BOOL)prepare:(NSString *)path {
  @synchronized(self) {
    if (finished) return NO;
    NSError *error = nil;
    NSString *parent = path.stringByDeletingLastPathComponent;
    if (![[NSFileManager defaultManager] createDirectoryAtPath:parent withIntermediateDirectories:YES attributes:nil error:&error]) {
      [self failLocked:[NSString stringWithFormat:@"create destination directory: %@", error.localizedDescription]];
      return NO;
    }
    char *pattern = strdup([[parent stringByAppendingPathComponent:@".photoscrawl-export-XXXXXX"] fileSystemRepresentation]);
    descriptor = mkstemp(pattern);
    if (descriptor < 0) {
      NSString *message = [NSString stringWithFormat:@"create export staging file: %s", strerror(errno)];
      free(pattern);
      [self failLocked:message];
      return NO;
    }
    stagingPath = [[[NSFileManager defaultManager] stringWithFileSystemRepresentation:pattern length:strlen(pattern)] copy];
    free(pattern);
    destinationPath = [path copy];
    return YES;
  }
}
- (void)registerRequest:(PHAssetResourceDataRequestID)value {
  BOOL failed;
  @synchronized(self) {
    requestID = value;
    if (value == PHInvalidAssetResourceDataRequestID) {
      [self failLocked:@"PhotoKit did not start the original resource request"];
    }
    failed = errorDescription != nil;
  }
  // Cancellation or an immediate callback can win before the ID is returned.
  if (failed) [self cancelRequest];
}
- (void)receive:(NSData *)data {
  BOOL failed = NO;
  @synchronized(self) {
    if (finished) return;
    const char *bytes = data.bytes;
    NSUInteger remaining = data.length;
    while (remaining > 0) {
      ssize_t count = write(descriptor, bytes, remaining);
      if (count < 0 && errno == EINTR) continue;
      if (count <= 0) {
        [self failLocked:[NSString stringWithFormat:@"write exported original: %s", count == 0 ? "short write" : strerror(errno)]];
        failed = YES;
        break;
      }
      bytes += count;
      remaining -= count;
    }
  }
  if (failed) [self cancelRequest];
}
- (void)complete:(NSError *)error {
  @synchronized(self) {
    if (finished) return;
    if (error != nil) {
      [self failLocked:[NSString stringWithFormat:@"export original resource: %@", error.localizedDescription]];
      return;
    }
    int result = close(descriptor);
    descriptor = -1;
    if (result != 0) {
      [self failLocked:[NSString stringWithFormat:@"close exported original: %s", strerror(errno)]];
      return;
    }
    // Same-directory rename replaces atomically and leaves the destination on failure.
    if (rename(stagingPath.fileSystemRepresentation, destinationPath.fileSystemRepresentation) != 0) {
      [self failLocked:[NSString stringWithFormat:@"promote exported original: %s", strerror(errno)]];
      return;
    }
    finished = YES;
    dispatch_semaphore_signal(completed);
  }
}
@end

void *photoscrawl_export_create(void) {
  return [[PCOriginalExport alloc] init];
}
void photoscrawl_export_cancel(void *control) {
  @autoreleasepool { [(PCOriginalExport *)control cancel]; }
}
int photoscrawl_export_cancelled(void *control) {
  if (control == NULL) return 0;
  PCOriginalExport *state = (PCOriginalExport *)control;
  @synchronized(state) { return state->cancelled; }
}
void photoscrawl_export_release(void *control) {
  @autoreleasepool { [(PCOriginalExport *)control release]; }
}

int photoscrawl_export_original_resource(const char *identifier, const char *destination, int allowNetwork, void *control, char **errorOut) {
  @autoreleasepool {
    *errorOut = NULL;
    PCOriginalExport *state = (PCOriginalExport *)control;
    PHAssetResource *resource = [(PHAssetResource *)photoscrawl_copy_original_resource(identifier, control, errorOut) autorelease];
    if (resource == nil) return 0;
    NSString *path = destination == NULL ? @"" : [NSString stringWithUTF8String:destination];
    if (path.length == 0) {
      *errorOut = strdup("destination path is required");
      return 0;
    }
    path = [NSURL fileURLWithPath:path].path;
    if ([state prepare:path]) {
      PHAssetResourceRequestOptions *options = [[PHAssetResourceRequestOptions alloc] init];
      options.networkAccessAllowed = allowNetwork != 0;
      // Copied PhotoKit blocks retain state after the Go caller returns on cancel.
      PHAssetResourceDataRequestID value = [[PHAssetResourceManager defaultManager] requestDataForAssetResource:resource options:options dataReceivedHandler:^(NSData *data) {
        [state receive:data];
      } completionHandler:^(NSError *error) {
        [state complete:error];
      }];
      [options release];
      [state registerRequest:value];
    }
    dispatch_semaphore_wait(state->completed, DISPATCH_TIME_FOREVER);
    @synchronized(state) {
      if (state->errorDescription != nil) {
        *errorOut = strdup(state->errorDescription.UTF8String);
        return 0;
      }
    }
    return 1;
  }
}
