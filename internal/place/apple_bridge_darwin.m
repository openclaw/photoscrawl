#import <Foundation/Foundation.h>
#import <CoreLocation/CoreLocation.h>
#import <MapKit/MapKit.h>
#include <stdlib.h>
#include <string.h>

static NSString *pcPlaceString(NSString *value) {
  return value == nil ? @"" : value;
}

static char *pcPlaceCopyCString(NSString *value) {
  const char *utf8 = [pcPlaceString(value) UTF8String];
  if (utf8 == NULL) {
    utf8 = "";
  }
  return strdup(utf8);
}

static void pcPlaceSetError(char **errorOut, NSString *message) {
  if (errorOut == NULL) {
    return;
  }
  *errorOut = pcPlaceCopyCString(message);
}

static void pcPlaceSetString(NSMutableDictionary *dict, NSString *key, NSString *value) {
  if (value != nil && value.length > 0) {
    dict[key] = value;
  }
}

static NSString *pcPlaceFormattedAddress(NSDictionary *address) {
  NSMutableArray *parts = [NSMutableArray array];
  NSString *street = @"";
  NSString *subThoroughfare = address[@"sub_thoroughfare"];
  NSString *thoroughfare = address[@"thoroughfare"];
  if (subThoroughfare.length > 0 && thoroughfare.length > 0) {
    street = [NSString stringWithFormat:@"%@ %@", subThoroughfare, thoroughfare];
  } else if (thoroughfare.length > 0) {
    street = thoroughfare;
  }
  NSArray *values = @[
    address[@"name"] ?: @"",
    street,
    address[@"sub_locality"] ?: @"",
    address[@"locality"] ?: @"",
    address[@"administrative_area"] ?: @"",
    address[@"country"] ?: @""
  ];
  for (NSString *value in values) {
    if (value.length > 0 && ![parts containsObject:value]) {
      [parts addObject:value];
    }
  }
  return [parts componentsJoinedByString:@", "];
}

static NSDictionary *pcPlaceAddress(CLPlacemark *placemark, NSString *source) {
  if (placemark == nil) {
    return nil;
  }
  NSMutableDictionary *address = [NSMutableDictionary dictionary];
  pcPlaceSetString(address, @"name", placemark.name);
  pcPlaceSetString(address, @"thoroughfare", placemark.thoroughfare);
  pcPlaceSetString(address, @"sub_thoroughfare", placemark.subThoroughfare);
  pcPlaceSetString(address, @"locality", placemark.locality);
  pcPlaceSetString(address, @"sub_locality", placemark.subLocality);
  pcPlaceSetString(address, @"administrative_area", placemark.administrativeArea);
  pcPlaceSetString(address, @"sub_administrative_area", placemark.subAdministrativeArea);
  pcPlaceSetString(address, @"postal_code", placemark.postalCode);
  pcPlaceSetString(address, @"country", placemark.country);
  pcPlaceSetString(address, @"iso_country_code", placemark.ISOcountryCode);
  if (placemark.timeZone != nil) {
    pcPlaceSetString(address, @"time_zone", placemark.timeZone.name);
  }
  NSMutableArray *areas = [NSMutableArray array];
  for (NSString *area in placemark.areasOfInterest ?: @[]) {
    if (area.length > 0) {
      [areas addObject:area];
    }
  }
  if (areas.count > 0) {
    address[@"areas_of_interest"] = areas;
  }
  pcPlaceSetString(address, @"source", source);
  pcPlaceSetString(address, @"formatted", pcPlaceFormattedAddress(address));
  return address;
}

#if __MAC_OS_X_VERSION_MAX_ALLOWED >= 260000
static NSDictionary *pcPlaceMapItemAddress(MKMapItem *item, NSString *source) API_AVAILABLE(macos(26.0)) {
  if (item == nil) {
    return nil;
  }
  NSMutableDictionary *address = [NSMutableDictionary dictionary];
  MKAddressRepresentations *representations = item.addressRepresentations;
  MKAddress *mapAddress = item.address;
  pcPlaceSetString(address, @"name", item.name);
  pcPlaceSetString(address, @"locality", representations.cityName);
  pcPlaceSetString(address, @"country", representations.regionName);
  pcPlaceSetString(address, @"iso_country_code", representations.regionCode);
  NSString *formatted = mapAddress.fullAddress;
  if (formatted.length == 0) {
    formatted = [representations fullAddressIncludingRegion:YES singleLine:YES];
  }
  pcPlaceSetString(address, @"formatted", formatted);
  pcPlaceSetString(address, @"source", source);
  return address.count > 1 ? address : nil;
}
#endif

static NSDictionary *pcPlaceCoordinate(CLLocation *location) {
  if (location == nil) {
    return nil;
  }
  return @{
    @"latitude": @(location.coordinate.latitude),
    @"longitude": @(location.coordinate.longitude)
  };
}

static NSDictionary *pcPlaceCandidate(MKMapItem *item, CLLocation *origin) {
  if (item == nil) {
    return nil;
  }
  NSString *name = item.name;
  if (name == nil || name.length == 0) {
    return nil;
  }

  NSMutableDictionary *candidate = [NSMutableDictionary dictionary];
  candidate[@"name"] = name;
  if (@available(macOS 10.15, *)) {
    pcPlaceSetString(candidate, @"category", item.pointOfInterestCategory);
  }
  CLLocation *location = nil;
  NSDictionary *address = nil;
#if __MAC_OS_X_VERSION_MAX_ALLOWED >= 260000
  if (@available(macOS 26.0, *)) {
    location = item.location;
    address = pcPlaceMapItemAddress(item, @"apple_mapkit_local_search");
  } else
#endif
  {
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
    MKPlacemark *placemark = item.placemark;
#pragma clang diagnostic pop
    location = placemark.location;
    address = pcPlaceAddress(placemark, @"apple_mapkit_local_search");
  }
  if (location != nil && origin != nil) {
    candidate[@"distance_m"] = @([location distanceFromLocation:origin]);
    NSDictionary *coordinate = pcPlaceCoordinate(location);
    if (coordinate != nil) {
      candidate[@"coordinate"] = coordinate;
    }
  }
  if (address != nil) {
    candidate[@"address"] = address;
  }
  candidate[@"source"] = @"apple_mapkit_local_search";
  candidate[@"provenance"] = @[@"MKLocalPointsOfInterestRequest"];
  return candidate;
}

static BOOL pcPlaceWait(BOOL *done, NSTimeInterval timeoutSeconds) {
  NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:timeoutSeconds];
  NSRunLoop *runLoop = [NSRunLoop currentRunLoop];
  while (!*done && [deadline timeIntervalSinceNow] > 0) {
    [runLoop runMode:NSDefaultRunLoopMode beforeDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
  }
  return *done;
}

char *photoscrawl_place_context_json(const char *requestJSON, char **errorOut) {
  @autoreleasepool {
    if (errorOut != NULL) {
      *errorOut = NULL;
    }
    NSString *requestString = requestJSON == NULL ? @"" : [NSString stringWithUTF8String:requestJSON];
    NSData *requestData = [requestString dataUsingEncoding:NSUTF8StringEncoding];
    NSError *jsonError = nil;
    NSDictionary *request = requestData == nil ? nil : [NSJSONSerialization JSONObjectWithData:requestData options:0 error:&jsonError];
    if (![request isKindOfClass:[NSDictionary class]]) {
      pcPlaceSetError(errorOut, [NSString stringWithFormat:@"decode place request: %@", jsonError.localizedDescription]);
      return NULL;
    }

    double latitude = [request[@"latitude"] doubleValue];
    double longitude = [request[@"longitude"] doubleValue];
    double radius = [request[@"radius_meters"] doubleValue];
    if (radius <= 0) {
      radius = 150;
    }
    CLLocation *origin = [[CLLocation alloc] initWithLatitude:latitude longitude:longitude];
    NSMutableDictionary *result = [NSMutableDictionary dictionary];
    NSMutableArray *candidates = [NSMutableArray array];

#if __MAC_OS_X_VERSION_MAX_ALLOWED >= 260000
    if (@available(macOS 26.0, *)) {
      __block NSArray<MKMapItem *> *mapItems = nil;
      __block NSError *geocodeError = nil;
      __block BOOL geocodeDone = NO;
      MKReverseGeocodingRequest *request = [[MKReverseGeocodingRequest alloc] initWithLocation:origin];
      [request getMapItemsWithCompletionHandler:^(NSArray<MKMapItem *> * _Nullable found, NSError * _Nullable error) {
        mapItems = [found retain];
        geocodeError = [error retain];
        geocodeDone = YES;
      }];
      if (!pcPlaceWait(&geocodeDone, 20.0)) {
        [request cancel];
        pcPlaceSetError(errorOut, @"Apple reverse geocode timed out");
        [mapItems release];
        [geocodeError release];
        [request release];
        return NULL;
      } else if (geocodeError != nil) {
        pcPlaceSetError(errorOut, [NSString stringWithFormat:@"Apple reverse geocode failed: %@", geocodeError.localizedDescription]);
        [mapItems release];
        [geocodeError release];
        [request release];
        return NULL;
      } else if (mapItems.count > 0) {
        NSDictionary *address = pcPlaceMapItemAddress(mapItems.firstObject, @"apple_mapkit_reverse");
        if (address != nil) {
          result[@"address"] = address;
        }
      } else {
        pcPlaceSetError(errorOut, @"Apple reverse geocode returned no map items");
        [mapItems release];
        [geocodeError release];
        [request release];
        return NULL;
      }
      [mapItems release];
      [geocodeError release];
      [request release];
    } else
#endif
    {
      __block NSArray<CLPlacemark *> *placemarks = nil;
      __block NSError *geocodeError = nil;
      __block BOOL geocodeDone = NO;
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
      CLGeocoder *geocoder = [[CLGeocoder alloc] init];
      [geocoder reverseGeocodeLocation:origin completionHandler:^(NSArray<CLPlacemark *> * _Nullable found, NSError * _Nullable error) {
        placemarks = [found retain];
        geocodeError = [error retain];
        geocodeDone = YES;
      }];
      if (!pcPlaceWait(&geocodeDone, 20.0)) {
        [geocoder cancelGeocode];
        pcPlaceSetError(errorOut, @"Apple reverse geocode timed out");
        [placemarks release];
        [geocodeError release];
        [geocoder release];
        return NULL;
      } else if (geocodeError != nil) {
        pcPlaceSetError(errorOut, [NSString stringWithFormat:@"Apple reverse geocode failed: %@", geocodeError.localizedDescription]);
        [placemarks release];
        [geocodeError release];
        [geocoder release];
        return NULL;
      } else if (placemarks.count > 0) {
        CLPlacemark *placemark = placemarks.firstObject;
        NSDictionary *address = pcPlaceAddress(placemark, @"apple_corelocation_reverse");
        if (address != nil) {
          result[@"address"] = address;
        }
      } else {
        pcPlaceSetError(errorOut, @"Apple reverse geocode returned no placemarks");
        [placemarks release];
        [geocodeError release];
        [geocoder release];
        return NULL;
      }
      [placemarks release];
      [geocodeError release];
      [geocoder release];
#pragma clang diagnostic pop
    }

    if (@available(macOS 11.0, *)) {
      __block MKLocalSearchResponse *searchResponse = nil;
      __block NSError *searchError = nil;
      __block BOOL searchDone = NO;
      MKLocalPointsOfInterestRequest *poiRequest = [[MKLocalPointsOfInterestRequest alloc] initWithCenterCoordinate:origin.coordinate radius:radius];
      MKLocalSearch *search = [[MKLocalSearch alloc] initWithPointsOfInterestRequest:poiRequest];
      [search startWithCompletionHandler:^(MKLocalSearchResponse * _Nullable response, NSError * _Nullable error) {
        searchResponse = [response retain];
        searchError = [error retain];
        searchDone = YES;
      }];
      if (!pcPlaceWait(&searchDone, 20.0)) {
        [search cancel];
        pcPlaceSetError(errorOut, @"Apple nearby POI search timed out");
        [searchResponse release];
        [searchError release];
        return NULL;
      } else if (searchError != nil) {
        if ([searchError.domain isEqualToString:MKErrorDomain] && searchError.code == MKErrorPlacemarkNotFound) {
          result[@"poi_status"] = @"none";
          result[@"poi_reason"] = @"apple_mapkit_placemark_not_found";
        } else {
          pcPlaceSetError(errorOut, [NSString stringWithFormat:@"Apple nearby POI search failed: %@", searchError.localizedDescription]);
          [searchResponse release];
          [searchError release];
          return NULL;
        }
      } else {
        for (MKMapItem *item in searchResponse.mapItems) {
          NSDictionary *candidate = pcPlaceCandidate(item, origin);
          if (candidate != nil) {
            [candidates addObject:candidate];
          }
        }
        result[@"poi_status"] = candidates.count > 0 ? @"found" : @"none";
      }
      [searchResponse release];
      [searchError release];
    } else {
      pcPlaceSetError(errorOut, @"Apple nearby POI search requires macOS 11 or newer");
      return NULL;
    }

    if (candidates.count > 0) {
      result[@"poi_candidates"] = candidates;
    }

    NSError *encodeError = nil;
    NSData *data = [NSJSONSerialization dataWithJSONObject:result options:0 error:&encodeError];
    if (data == nil) {
      pcPlaceSetError(errorOut, [NSString stringWithFormat:@"encode place result: %@", encodeError.localizedDescription]);
      return NULL;
    }
    char *json = malloc(data.length + 1);
    if (json == NULL) {
      pcPlaceSetError(errorOut, @"allocate place JSON");
      return NULL;
    }
    memcpy(json, data.bytes, data.length);
    json[data.length] = '\0';
    return json;
  }
}
