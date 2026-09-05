#ifndef PHOTOSCRAWL_ORIGINAL_EXPORT_H
#define PHOTOSCRAWL_ORIGINAL_EXPORT_H

void *photoscrawl_export_create(void);
void photoscrawl_export_cancel(void *control);
int photoscrawl_export_cancelled(void *control);
void photoscrawl_export_release(void *control);
int photoscrawl_export_original_resource(const char *identifier, const char *destination, int allowNetwork, void *control, char **errorOut);

// Returns a retained PHAssetResource; the export bridge owns the reference.
void *photoscrawl_copy_original_resource(const char *identifier, void *control, char **errorOut);

#endif
