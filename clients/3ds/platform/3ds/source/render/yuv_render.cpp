#include "yuv_render.hpp"
#include "config.h"
#include "logger.h"
#include <string.h>

static bool  s_y2r_ok    = false;
static Handle s_done_evt = 0;

// Max temp buffer for CPU fallback — sized for 320x240 (largest preset).
// 320*240*2 = 153600 bytes.
static uint8_t *s_temp_buf = NULL;
#define TEMP_BUF_SIZE (320 * 240 * 2)

// BLOCK_8_BY_8 fast path: Y2R writes tiled RGB565 directly into the C3D texture.
static bool yuv_render_hw_direct(Image_data *img, const uint8_t *y,
                                  const uint8_t *u, const uint8_t *v,
                                  int width, int height) {
    const u16 sw  = (u16)width;
    const u16 sh  = (u16)height;
    const u16 tw  = TEX_WIDTH;   // 512
    const u32 y_rows  = (u32)sw * sh;
    const u32 uv_rows = (u32)(sw / 2) * (sh / 2);

    Y2RU_StopConversion();

    Y2RU_ConversionParams params;
    memset(&params, 0, sizeof(params));
    params.input_format         = INPUT_YUV420_INDIV_8;
    params.output_format        = OUTPUT_RGB_16_565;
    params.rotation             = ROTATION_NONE;
    params.block_alignment      = BLOCK_8_BY_8;
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

    const s16 transfer_unit = (s16)(sw * 8 * 2);
    const s16 transfer_gap  = (s16)((tw - sw) * 8 * 2);
    const u32 tex_buf_sz    = (u32)tw * TEX_HEIGHT * 2;

    rc = Y2RU_SetReceiving(img->c2d.tex->data, tex_buf_sz, transfer_unit, transfer_gap);
    if (R_FAILED(rc)) { Log("y2r: SetReceiving 0x%08lX\n", rc); return false; }

    rc = Y2RU_StartConversion();
    if (R_FAILED(rc)) {
        Log("y2r: StartConversion 0x%08lX\n", rc);
        return false;
    }

    svcWaitSynchronization(s_done_evt, 5000000LL);
    C3D_TexFlush(img->c2d.tex);

    img->subtex->width  = sw;
    img->subtex->height = sh;
    img->subtex->left   = 0.0f;
    img->subtex->top    = 1.0f;
    img->subtex->right  = (float)sw / tw;
    img->subtex->bottom = 1.0f - (float)sh / (float)TEX_HEIGHT;
    img->c2d.subtex     = img->subtex;

    return true;
}

// LINEAR fallback: Y2R outputs linear RGB565, then CPU swizzles.
static bool yuv_render_hw_linear(Image_data *img, const uint8_t *y,
                                   const uint8_t *u, const uint8_t *v,
                                   int width, int height) {
    const u16 sw = (u16)width;
    const u16 sh = (u16)height;

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
                                                  sw, sh,
                                                  TEX_WIDTH, TEX_HEIGHT, GPU_RGB565);
    return r.code == 0;
}

// Pure-CPU fallback.
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

static void yuv_render_cpu(Image_data *img, const uint8_t *yuv420p,
                           int width, int height) {
    const int w = width;
    const int h = height;
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

    s_temp_buf = (uint8_t *)linearMemAlign(TEMP_BUF_SIZE, 0x80);
    if (!s_temp_buf) {
        Log("yuv_render: temp buffer alloc failed\n");
        return -1;
    }

    Result rc = y2rInit();
    if (R_FAILED(rc)) {
        Log("yuv_render: y2rInit failed 0x%08lX, CPU fallback\n", rc);
        return 0;
    }

    rc = Y2RU_GetTransferEndEvent(&s_done_evt);
    if (R_FAILED(rc)) {
        Log("yuv_render: GetTransferEndEvent failed 0x%08lX, CPU fallback\n", rc);
        y2rExit();
        return 0;
    }

    s_y2r_ok = true;
    Log("yuv_render: Y2R hardware ready\n");
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

void yuv_render_frame(Image_data *img, const uint8_t *yuv420p, int width, int height) {
    if (!yuv420p || !img || width <= 0 || height <= 0) return;

    const int w = width;
    const int h = height;
    const uint8_t *y = yuv420p;
    const uint8_t *u = yuv420p + (w * h);
    const uint8_t *v = yuv420p + (w * h) + (w * h / 4);

    if (s_y2r_ok) {
        if (yuv_render_hw_direct(img, y, u, v, w, h))
            return;
        if (yuv_render_hw_linear(img, y, u, v, w, h))
            return;
    }
    yuv_render_cpu(img, yuv420p, w, h);
}