#pragma once
#include <3ds.h>
#include "../decode/mvd_h264.hpp"

// Initialize Y2R hardware for NV12 → tiled RGB565 conversion.
int y2r_init(void);
void y2r_deinit(void);

// Convert one NV12 frame to GPU-tiled RGB565 and write it into tex_data.
// tex_data must be linearAlloc'd and large enough for TEX_WIDTH*TEX_HEIGHT*2 bytes.
// Returns 0 on success.
int y2r_upload(const MVD_Frame *frame, void *tex_data);
