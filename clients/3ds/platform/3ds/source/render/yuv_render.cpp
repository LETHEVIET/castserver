#include "yuv_render.hpp"
#include "config.h"
#include "logger.h"
#include <string.h>

static bool  s_y2r_ok    = false;
static Handle s_done_evt = 0;

// BLOCK_8_BY_8 fast path: Y2R writes tiled RGB565 directly into the C3D texture,
// so no temp buffer or CPU swizzle is needed.
//
// Tiled format: 8 rows of (stream_width pixels) per block-row, with each pixel
// 2 bytes.  Block-rows are padded to TEX_WIDTH pixels wide.  The DMA unit
// parameters tell Y2R how many bytes of real data to send per transfer and
// how many padding bytes to skip between units.
//
// For 256x192 into a 256x256 atlas:
//   unit_bytes  = stream_width * 8 * 2     = 256 * 8 * 2 = 4096
//   output_gap  = (tex_width - stream_width) * 8 * 2  = 0 (same width!)
//   tex_buf_sz  = tex_width * tex_height * 2 = 256 * 256 * 2 = 131072
//
// For 400x240 into a 512x256 atlas:
//   unit_bytes  = 400 * 8 * 2 = 6400
//   output_gap  = (512 - 400) * 8 * 2 = 1792
//   tex_buf_sz  = 512 * 256 * 2 = 262144

static bool yuv_render_hw_direct(Image_data *img, const uint8_t *y,
                                  const uint8_t *u, const uint8_t *v) {
    const u16 sw  = STREAM_WIDTH;
    const u16 sh  = STREAM_HEIGHT;
    const u16 tw  = TEX_WIDTH;
    const u32 y_rows  = (u32)sw * sh;
    const u32 uv_rows = (u32)(sw / 2) * (sh / 2);

    Y2RU_StopConversion();

    Y2RU_ConversionParams params;
    memset(&params, 0, sizeof(params));
    params.input_format         = INPUT_YUV420_INDIV_8;
    params.output_format        = OUTPUT_RGB_16_565;
    params.rotation             = ROTATION_NONE;
    params.block_alignment      = BLOCK_8_BY_8;  // GPU-tiled output
    params.input_line_width     = sw;
    params.input_lines          = sh;
    params.standard_coefficient = COEFFICIENT_ITU_R_BT_601_SCALING;
    params.unused               = 0;
    params.alpha                = 0xFF;

    Result rc = Y2RU_SetConversionParams(&params);
    if (R_FAILED(rc)) {
        Log("y2r: SetConversionParams failed 0x%08lX\n", rc);
        return false;
    }

    rc = Y2RU_SetSendingY((void *)y, y_rows, sw, 0);
    if (R_FAILED(rc)) { Log("y2r: SetSendingY 0x%08lX\n", rc); return false; }

    rc = Y2RU_SetSendingU((void *)u, uv_rows, sw / 2, 0);
    if (R_FAILED(rc)) { Log("y2r: SetSendingU 0x%08lX\n", rc); return false; }

    rc = Y2RU_SetSendingV((void *)v, uv_rows, sw / 2, 0);
    if (R_FAILED(rc)) { Log("y2r: SetSendingV 0x%08lX\n", rc); return false; }

    const s16 transfer_unit = (s16)(sw * 8 * 2);                   // bytes per DMA unit (8 row block)
    const s16 transfer_gap  = (s16)((tw - sw) * 8 * 2);            // padding between units
    const u32 tex_buf_sz  = (u32)tw * TEX_HEIGHT * 2;

    rc = Y2RU_SetReceiving(img->c2d.tex->data, tex_buf_sz, transfer_unit, transfer_gap);
    if (R_FAILED(rc)) { Log("y2r: SetReceiving 0x%08lX\n", rc); return false; }

    rc = Y2RU_StartConversion();
    if (R_FAILED(rc)) {
        Log("y2r: StartConversion 0x%08lX\n", rc);
        return false;
    }

    // 5 ms is plenty for a 256x192 Y2R DMA on real hardware.
    svcWaitSynchronization(s_done_evt, 5000000LL);

    C3D_TexFlush(img->c2d.tex);

    // Fix up sub-texture to cover the stream area within the atlas.
    img->subtex->width  = (u16)STREAM_WIDTH;
    img->subtex->height = (u16)STREAM_HEIGHT;
    img->subtex->left   = 0.0f;
    img->subtex->top    = 1.0f;
    img->subtex->right  = (float)STREAM_WIDTH  / TEX_WIDTH;
    img->subtex->bottom = 1.0f - (float)STREAM_HEIGHT / (float)TEX_HEIGHT;
    img->c2d.subtex     = img->subtex;

    return true;
}

// ---- LINEAR fallback: Y2R outputs linear RGB565, then CPU swizzles ----
static uint8_t *s_temp_buf = NULL;

static bool yuv_render_hw_linear(Image_data *img, const uint8_t *y,
                                   const uint8_t *u, const uint8_t *v) {
    const u16 sw = STREAM_WIDTH;
    const u16 sh = STREAM_HEIGHT;

    Y2RU_StopConversion();

    Y2RU_ConversionParams params;
    memset(&params, 0, sizeof(params));
    params.input_format         = INPUT_YUV420_INDIV_8;
    params.output_format        = OUTPUT_RGB_16_565;
    params.rotation             = ROTATION_NONE;
    params.block_alignment      = BLOCK_LINE;
    params.input_line_width     = sw;
    params.input_lines          = sh;
    params.standard_coefficient = COEFFICIENT_ITU_R_BT_601_SCALING;
    params.alpha                = 0xFF;

    Result rc = Y2RU_SetConversionParams(&params);
    if (R_FAILED(rc)) return false;

    rc = Y2RU_SetSendingY((void *)y, (u32)(sw * sh), sw, 0);
    if (R_FAILED(rc)) return false;
    rc = Y2RU_SetSendingU((void *)u, (u32)(sw * sh / 4), sw / 2, 0);
    if (R_FAILED(rc)) return false;
    rc = Y2RU_SetSendingV((void *)v, (u32)(sw * sh / 4), sw / 2, 0);
    if (R_FAILED(rc)) return false;

    rc = Y2RU_SetReceiving(s_temp_buf, (u32)(sw * sh * 2), (u16)(sw * 2), 0);
    if (R_FAILED(rc)) return false;

    rc = Y2RU_StartConversion();
    if (R_FAILED(rc)) return false;

    svcWaitSynchronization(s_done_evt, 5000000LL);

    Result_with_string r = Draw_set_texture_data(img, s_temp_buf,
                                                  STREAM_WIDTH, STREAM_HEIGHT,
                                                  TEX_WIDTH, TEX_HEIGHT, GPU_RGB565);
    return r.code == 0;
}

// ---- Pure-CPU fallback (emulator or no Y2R) ----
static inline uint16_t yuv_to_rgb565(int y, int u, int v) {
    int c = y - 16;
    int d = u - 128;
    int e = v - 128;

    int r = (298 * c + 409 * e + 128) >> 8;
    int g = (298 * c - 100 * d - 208 * e + 128) >> 8;
    int b = (298 * c + 516 * d + 128) >> 8;

    if (r < 0) r = 0; else if (r > 255) r = 255;
    if (g < 0) g = 0; else if (g > 255) g = 255;
    if (b < 0) b = 0; else if (b > 255) b = 255;

    return (uint16_t)(((r >> 3) << 11) | ((g >> 2) << 5) | (b >> 3));
}

static void yuv_render_cpu(Image_data *img, const uint8_t *yuv420p) {
    const int w = STREAM_WIDTH;
    const int h = STREAM_HEIGHT;
    const uint8_t *y_plane = yuv420p;
    const uint8_t *u_plane = yuv420p + (w * h);
    const uint8_t *v_plane = yuv420p + (w * h) + (w * h / 4);

    uint16_t *rowbuf = (uint16_t *)s_temp_buf;

    for (int row = 0; row < h; row++) {
        for (int col = 0; col < w; col++) {
            int y = y_plane[row * w + col];
            int u = u_plane[(row / 2) * (w / 2) + (col / 2)];
            int v = v_plane[(row / 2) * (w / 2) + (col / 2)];
            rowbuf[row * w + col] = yuv_to_rgb565(y, u, v);
        }
    }

    Draw_set_texture_data(img, (uint8_t *)rowbuf,
                          w, h, TEX_WIDTH, TEX_HEIGHT, GPU_RGB565);
}

int yuv_render_init(void) {
    s_y2r_ok    = false;
    s_temp_buf  = NULL;
    s_done_evt  = 0;

    // Allocate temp buffer for LINEAR fallback and CPU path.
    // 256*192*2 = 98304 bytes in linear memory.
    s_temp_buf = (uint8_t *)linearMemAlign((size_t)STREAM_WIDTH * STREAM_HEIGHT * 2, 0x80);
    if (!s_temp_buf) {
        Log("yuv_render: temp buffer alloc failed\n");
        return -1;
    }

    Result rc = y2rInit();
    if (R_FAILED(rc)) {
        Log("yuv_render: y2rInit failed 0x%08lX, CPU fallback\n", rc);
        return 0;   // CPU fallback is acceptable
    }

    rc = Y2RU_GetTransferEndEvent(&s_done_evt);
    if (R_FAILED(rc)) {
        Log("yuv_render: GetTransferEndEvent failed 0x%08lX, CPU fallback\n", rc);
        y2rExit();
        return 0;
    }

    s_y2r_ok = true;
    Log("yuv_render: Y2R hardware ready (BLOCK_8_BY_8 path)\n");
    return 0;
}

void yuv_render_deinit(void) {
    if (s_y2r_ok) {
        y2rExit();
        s_y2r_ok = false;
    }
    if (s_temp_buf) {
        linearFree(s_temp_buf);
        s_temp_buf = NULL;
    }
}

bool yuv_render_hw_available(void) { return s_y2r_ok; }

void yuv_render_frame(Image_data *img, const uint8_t *yuv420p) {
    if (!yuv420p || !img) return;

    const int w = STREAM_WIDTH;
    const int h = STREAM_HEIGHT;
    const uint8_t *y = yuv420p;
    const uint8_t *u = yuv420p + (w * h);
    const uint8_t *v = yuv420p + (w * h) + (w * h / 4);

    if (s_y2r_ok) {
        // Fast path: Y2R DMA direct into GPU-tiled texture (BLOCK_8_BY_8).
        if (yuv_render_hw_direct(img, y, u, v))
            return;
        // If BLOCK_8_BY_8 path failed, try LINEAR + swizzle.
        if (yuv_render_hw_linear(img, y, u, v))
            return;
    }
    // CPU fallback
    yuv_render_cpu(img, yuv420p);
}