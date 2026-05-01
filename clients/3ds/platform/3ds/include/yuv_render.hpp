#pragma once
#include <3ds.h>
#include "draw.h"
#include "config.h"

// Initialize Y2R hardware (or prepare CPU fallback buffers).
// Returns 0 on success.
int yuv_render_init(void);
void yuv_render_deinit(void);

// Convert a YUV420p frame (STREAM_WIDTH x STREAM_HEIGHT) into the C3D texture.
// Prefers Y2R BLOCK_8_BY_8 (DMA direct to tiled GPU texture), falls back
// to Y2R LINEAR + CPU swizzle, then to pure-CPU.
void yuv_render_frame(Image_data *img, const uint8_t *yuv420p);

// Returns true if Y2R hardware is available and initialised.
bool yuv_render_hw_available(void);