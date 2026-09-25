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
static void confirmPartial(NSData *expected) {
  NSString *directory = [root stringByAppendingPathComponent:@"out"];
  NSArray *entries = [[NSFileManager defaultManager] contentsOfDirectoryAtPath:directory error:NULL];
  NSUInteger count = 0;
  for (NSString *entry in entries) {
    if (![entry hasPrefix:@".photoscrawl-export-"]) continue;
    NSData *actual = [NSData dataWithContentsOfFile:[directory stringByAppendingPathComponent:entry]];
    if (![actual isEqualToData:expected]) abort();
    count++;
  }
  if (count != 1) abort();
  mark(@"partial");
}
static void waitForMarker(NSString *name) {
  NSString *path = [root stringByAppendingPathComponent:name];
  NSTimeInterval deadline = NSProcessInfo.processInfo.systemUptime + 5;
  while (![[NSFileManager defaultManager] fileExistsAtPath:path]) {
    if (NSProcessInfo.processInfo.systemUptime >= deadline) abort();
    usleep(5000);
  }
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
- (NSString *)uniformTypeIdentifier { return @"public.jpeg"; }
- (NSString *)originalFilename { return @"fixture.jpeg"; }
- (NSString *)assetLocalIdentifier { return @"fixture-asset"; }
@end
static id resources(id self, SEL selector, id asset) { return @[[PCFixtureResource new]]; }

@interface PCFixtureFetchResult : NSObject
@property(nonatomic, strong) NSArray *objects;
@end
@implementation PCFixtureFetchResult
- (NSUInteger)count { return self.objects.count; }
- (void)enumerateObjectsUsingBlock:(void (^)(id, NSUInteger, BOOL *))block {
  BOOL stop = NO;
  [self.objects enumerateObjectsUsingBlock:^(id object, NSUInteger index, BOOL *innerStop) {
    block(object, index, &stop);
    if (stop) *innerStop = YES;
  }];
}
@end
static PCFixtureFetchResult *result(NSArray *objects) {
  PCFixtureFetchResult *value = [PCFixtureFetchResult new];
  value.objects = objects;
  return value;
}

@interface PCFixtureAsset : NSObject
@property(nonatomic, copy) NSString *identifier;
@end
@implementation PCFixtureAsset
- (NSString *)localIdentifier { return self.identifier; }
- (PHAssetMediaType)mediaType { return PHAssetMediaTypeImage; }
- (PHAssetMediaSubtype)mediaSubtypes { return PHAssetMediaSubtypeNone; }
- (NSDate *)creationDate { return [NSDate dateWithTimeIntervalSince1970:1700000000]; }
- (NSDate *)modificationDate { return [NSDate dateWithTimeIntervalSince1970:1700000000]; }
- (NSUInteger)pixelWidth { return 100; }
- (NSUInteger)pixelHeight { return 100; }
- (NSTimeInterval)duration { return 0; }
- (BOOL)isFavorite { return NO; }
- (BOOL)isHidden { return NO; }
- (NSString *)burstIdentifier { return nil; }
- (BOOL)representsBurst { return NO; }
- (CLLocation *)location { return nil; }
- (PHAssetSourceType)sourceType { return PHAssetSourceTypeUserLibrary; }
@end

@interface PCFixtureAlbum : NSObject
@property(nonatomic, copy) NSString *identifier;
@property(nonatomic, copy) NSString *title;
@property(nonatomic) PHAssetCollectionType fixtureType;
@property(nonatomic) PHAssetCollectionSubtype fixtureSubtype;
@property(nonatomic, strong) NSArray *assets;
@end
@implementation PCFixtureAlbum
- (NSString *)localIdentifier { return self.identifier; }
- (NSString *)localizedTitle { return self.title; }
- (PHAssetCollectionType)assetCollectionType { return self.fixtureType; }
- (PHAssetCollectionSubtype)assetCollectionSubtype { return self.fixtureSubtype; }
@end

static PCFixtureAsset *fixtureAsset(NSString *identifier) {
  PCFixtureAsset *asset = [PCFixtureAsset new];
  asset.identifier = identifier;
  return asset;
}
static PCFixtureAlbum *fixtureAlbum(NSString *identifier, NSString *title, PHAssetCollectionType type, PHAssetCollectionSubtype subtype, NSArray *assets) {
  PCFixtureAlbum *album = [PCFixtureAlbum new];
  album.identifier = identifier;
  album.title = title;
  album.fixtureType = type;
  album.fixtureSubtype = subtype;
  album.assets = assets;
  return album;
}
static NSArray *fixtureAssets(void) {
  static NSArray *assets;
  static dispatch_once_t once;
  dispatch_once(&once, ^{ assets = @[fixtureAsset(@"fixture-asset-1"), fixtureAsset(@"fixture-asset-2")]; });
  return assets;
}
static NSArray *fixtureAlbums(void) {
  static NSArray *albums;
  static dispatch_once_t once;
  dispatch_once(&once, ^{
    NSArray *assets = fixtureAssets();
    albums = @[
      fixtureAlbum(@"album-regular", @"Regular", PHAssetCollectionTypeAlbum, PHAssetCollectionSubtypeAlbumRegular, assets),
      fixtureAlbum(@"album-shared", @"Shared", PHAssetCollectionTypeAlbum, PHAssetCollectionSubtypeAlbumCloudShared, @[assets[0]]),
      fixtureAlbum(@"album-smart", @"Smart", PHAssetCollectionTypeSmartAlbum, PHAssetCollectionSubtypeSmartAlbumFavorites, @[assets[1]])
    ];
  });
  return albums;
}
static id fetchAllAssets(id self, SEL selector, id options) {
  if (![mode isEqual:@"snapshot-albums"]) abort();
  return result(fixtureAssets());
}
static id fetchTopLevelCollections(id self, SEL selector, id options) {
  if (![mode isEqual:@"snapshot-albums"]) abort();
  return result(@[]);
}
static id fetchAlbumCollections(id self, SEL selector, PHAssetCollectionType type, PHAssetCollectionSubtype subtype, id options) {
  if (![mode isEqual:@"snapshot-albums"]) abort();
  NSMutableArray *matches = [NSMutableArray array];
  for (PCFixtureAlbum *album in fixtureAlbums()) {
    if (album.fixtureType != type) continue;
    if (subtype == PHAssetCollectionSubtypeAny || album.fixtureSubtype == subtype) [matches addObject:album];
  }
  return result(matches);
}
static id fetchAssetsInCollection(id self, SEL selector, PCFixtureAlbum *album, id options) {
  if (![mode isEqual:@"snapshot-albums"]) abort();
  return result(album.assets);
}
static id fetchContainingAlbums(id self, SEL selector, PCFixtureAsset *asset, PHAssetCollectionType type, id options) {
  if (![mode isEqual:@"snapshot-albums"]) abort();
  mark(@"containing-album-fetch");
  return result(@[fixtureAlbums()[0]]);
}

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
  if ([mode hasPrefix:@"limited-"]) {
    if (((PHAssetResourceRequestOptions *)options).networkAccessAllowed) abort();
    if ([mode isEqual:@"limited-cancel"]) {
      @synchronized(pending) { pending[@(request)] = @[[data copy], [complete copy]]; }
      NSData *partial = [@"partial" dataUsingEncoding:NSUTF8StringEncoding];
      data(partial);
      confirmPartial(partial);
    } else if ([mode isEqual:@"limited-over-single"]) {
      data(bytes);
      complete(nil);
    } else if ([mode isEqual:@"limited-exact"] || [mode isEqual:@"limited-over-chunked"]) {
      NSData *first = [bytes subdataWithRange:NSMakeRange(0, 7)];
      data(first);
      confirmPartial(first);
      data([bytes subdataWithRange:NSMakeRange(7, bytes.length - 7)]);
      complete(nil);
    } else {
      abort();
    }
    mark(@"streamed");
  } else if ([mode isEqual:@"stall"] || [mode isEqual:@"delayed-id"] || ([mode isEqual:@"retry"] && request == 1)) {
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
  if ([mode isEqual:@"limited-cancel"]) {
    // The Go driver releases this barrier only after the export API returns.
    dispatch_async(dispatch_get_global_queue(0, 0), ^{
      @autoreleasepool {
        waitForMarker(@"export-returned");
        late();
      }
    });
  } else if ([mode isEqual:@"retry"]) {
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
	  replace(PHAsset.class, @selector(fetchAssetsWithOptions:), (IMP)fetchAllAssets);
	  replace(PHAsset.class, @selector(fetchAssetsInAssetCollection:options:), (IMP)fetchAssetsInCollection);
	  replace(PHCollectionList.class, @selector(fetchTopLevelUserCollectionsWithOptions:), (IMP)fetchTopLevelCollections);
	  replace(PHAssetCollection.class, @selector(fetchAssetCollectionsWithType:subtype:options:), (IMP)fetchAlbumCollections);
	  replace(PHAssetCollection.class, @selector(fetchAssetCollectionsContainingAsset:withType:options:), (IMP)fetchContainingAlbums);
    replace(PHAssetResource.class, @selector(assetResourcesForAsset:), (IMP)resources);
    replace(PHAssetResourceManager.class, @selector(defaultManager), (IMP)manager);
    mark(@"loaded");
  }
}
