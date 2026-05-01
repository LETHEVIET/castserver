#pragma once
#include <3ds.h>
#include "draw.h"

// One 400×240 YUV420p frame = 144000 bytes
#define YUV_FRAME_SIZE (400 * 240 * 3 / 2)

// Initialize Y2R (hardware) or CPU fallback.
int yuv_render_init(void);
void yuv_render_deinit(void);

// Convert a YUV420p frame into the C3D texture.
// Uses Y2R hardware on real 3DS; CPU fallback in emulator.
void yuv_render_frame(Image_data *img, const uint8_t *yuv420p,
                      int width, int height);

// Returns true if Y2R hardware is available.
bool yuv_render_hw_available(void);
