// Test-only PhotoKit replacement. Linked as a required dylib into proof binaries.
#import <Foundation/Foundation.h>
#import <Photos/Photos.h>
#import <objc/runtime.h>
#include <signal.h>
#include <sys/resource.h>
#include <unistd.h>

static NSString *root;
static NSString *mode;
static int requests;
static NSMutableDictionary *pending;

static void mark(NSString *name) {
  NSString *path = [root stringByAppendingPathComponent:name];
  if (![[NSFileManager defaultManager] createFileAtPath:path contents:[NSData data] attributes:nil]) abort();
}
static PHAuthorizationStatus authorization(id self, SEL selector, ...) {
  return [mode hasPrefix:@"auth"] ? PHAuthorizationStatusNotDetermined : PHAuthorizationStatusAuthorized;
}
static void authorize(id self, SEL selector, PHAccessLevel level, void (^handler)(PHAuthorizationStatus)) {
  mark(@"authorization");
  int64_t delay = [mode isEqual:@"auth-slow"] ? 16 * NSEC_PER_SEC : 300 * NSEC_PER_MSEC;
  dispatch_after(dispatch_time(DISPATCH_TIME_NOW, delay), dispatch_get_global_queue(0, 0), ^{
    handler(PHAuthorizationStatusAuthorized);
    mark(@"late");
  });
}
static id fetch(id self, SEL selector, NSArray *identifiers, id options) {
  if (![identifiers isEqual:@[@"synthetic"]]) abort();
  return @[[NSObject new]];
}
@interface PCFixtureResource : NSObject
- (PHAssetResourceType)type;
@end
@implementation PCFixtureResource
- (PHAssetResourceType)type { return PHAssetResourceTypePhoto; }
@end
static id resources(id self, SEL selector, id asset) { return @[[PCFixtureResource new]]; }

@interface PCFixtureManager : NSObject
@end
@implementation PCFixtureManager
- (PHAssetResourceDataRequestID)requestDataForAssetResource:(id)resource options:(id)options dataReceivedHandler:(void (^)(NSData *))data completionHandler:(void (^)(NSError *))complete {
  int request = ++requests;
  mark(@"requested");
    if ([mode isEqual:@"write-error"]) {
      signal(SIGXFSZ, SIG_IGN);
      struct rlimit limit = {4, 4};
      if (setrlimit(RLIMIT_FSIZE, &limit) != 0) abort();
    }

  NSData *bytes = [@"synthetic-original" dataUsingEncoding:NSUTF8StringEncoding];
  if ([mode isEqual:@"stall"] || [mode isEqual:@"delayed-id"] || ([mode isEqual:@"retry"] && request == 1)) {
    @synchronized(pending) { pending[@(request)] = @[[data copy], [complete copy]]; }
    data([@"partial" dataUsingEncoding:NSUTF8StringEncoding]);
    if ([mode isEqual:@"delayed-id"]) usleep(200000);
  } else if ([mode isEqual:@"error"]) {
    @autoreleasepool {
      complete([NSError errorWithDomain:@"synthetic" code:1 userInfo:@{NSLocalizedDescriptionKey: @"synthetic provider failure"}]);
    }
  } else if ([mode isEqual:@"slow"]) {
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 250 * NSEC_PER_MSEC), dispatch_get_global_queue(0, 0), ^{
      data(bytes);
      complete(nil);
    });
  } else {
    data(bytes);
    complete(nil);
  }
  return request;
}
- (void)cancelDataRequest:(PHAssetResourceDataRequestID)request {
  mark(@"cancelled");
  NSArray *callbacks;
  @synchronized(pending) {
    callbacks = pending[@(request)];
    [pending removeObjectForKey:@(request)];
  }
  if (callbacks == nil) return;
  void (^late)(void) = ^{
    void (^data)(NSData *) = callbacks[0];
    void (^complete)(NSError *) = callbacks[1];
    data([@"late-old-request" dataUsingEncoding:NSUTF8StringEncoding]);
    complete(nil);
    mark(@"late");
  };
  if ([mode isEqual:@"retry"]) {
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 300 * NSEC_PER_MSEC), dispatch_get_global_queue(0, 0), late);
  } else {
    late();
  }
}
@end
static id manager(id self, SEL selector) {
  static PCFixtureManager *instance;
  static dispatch_once_t once;
  dispatch_once(&once, ^{ instance = [PCFixtureManager new]; });
  return instance;
}
static void replace(Class cls, SEL selector, IMP implementation) {
  Method method = class_getClassMethod(cls, selector);
  if (method == NULL) abort();
  method_setImplementation(method, implementation);
}
__attribute__((constructor)) static void installFixture(void) {
  @autoreleasepool {
    const char *directory = getenv("PHOTOSCRAWL_NATIVE_FIXTURE_DIR");
    const char *selected = getenv("PHOTOSCRAWL_NATIVE_FIXTURE_MODE");
    if (directory == NULL || selected == NULL) abort();
    root = [NSString stringWithUTF8String:directory];
    mode = [NSString stringWithUTF8String:selected];
    pending = [NSMutableDictionary new];
    replace(PHPhotoLibrary.class, @selector(authorizationStatus), (IMP)authorization);
    replace(PHPhotoLibrary.class, @selector(authorizationStatusForAccessLevel:), (IMP)authorization);
    replace(PHPhotoLibrary.class, @selector(requestAuthorizationForAccessLevel:handler:), (IMP)authorize);
    replace(PHAsset.class, @selector(fetchAssetsWithLocalIdentifiers:options:), (IMP)fetch);
    replace(PHAssetResource.class, @selector(assetResourcesForAsset:), (IMP)resources);
    replace(PHAssetResourceManager.class, @selector(defaultManager), (IMP)manager);
    mark(@"loaded");
  }
}
