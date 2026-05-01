#pragma once
#include <3ds.h>
#include <stdint.h>

// Decoded frame from MVDSTD — BGR565 row-major, 400×240 pixels.
// rgb565 points into a linearAlloc'd buffer owned by the mvd module.
typedef struct {
    uint8_t *rgb565;  // BGR565 output: 400 * 240 * 2 = 192 000 bytes
    uint32_t size;
    uint16_t width;
    uint16_t height;
} MVD_Frame;

// Initialize MVDSTD for H.264 Baseline decoding, 400×240 BGR565 output.
int mvd_h264_init(void);
void mvd_h264_deinit(void);

// Decode one complete H.264 access unit (Annex-B byte stream).
// Returns  0 when a frame was decoded and rendered (out is valid).
// Returns  1 when more input is needed (no frame ready yet).
// Returns -1 on error.
int mvd_h264_decode(const uint8_t *annex_b, uint32_t size, MVD_Frame *out);
