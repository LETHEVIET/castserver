#include "yuv_render.hpp"
#include "config.h"
#include "logger.h"
#include <string.h>

static bool s_y2r_ok = false;
static Handle s_done_evt = 0;

// Temporary buffer for Y2R LINE output (linear RGB565)
static uint8_t *s_temp_buf = NULL;
#define TEMP_BUF_SIZE (400 * 240 * 2)  // 192000 bytes

int yuv_render_init(void) {
    s_y2r_ok = false;
    s_temp_buf = NULL;

    s_temp_buf = (uint8_t *)linearMemAlign(TEMP_BUF_SIZE, 0x80);
    if (!s_temp_buf) {
        Log("yuv_render: temp buffer alloc failed\n");
        return -1;
    }

    Result rc = y2rInit();
    if (R_FAILED(rc)) {
        Log("yuv_render: y2rInit failed 0x%08lX, using CPU fallback\n", rc);
        // CPU fallback is acceptable — don't free temp_buf, we might use it
        return 0;
    }

    rc = Y2RU_GetTransferEndEvent(&s_done_evt);
    if (R_FAILED(rc)) {
        Log("yuv_render: GetTransferEndEvent failed 0x%08lX, using CPU fallback\n", rc);
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

// ---- Hardware path: Y2R LINEAR output -> temp buffer -> CPU swizzle ----
static bool yuv_render_hw(Image_data *img, const uint8_t *yuv420p,
                          int width, int height) {
    if (!s_y2r_ok || !s_temp_buf) return false;

    const uint8_t *y = yuv420p;
    const uint8_t *u = yuv420p + (width * height);
    const uint8_t *v = yuv420p + (width * height) + (width * height / 4);

    Y2RU_StopConversion();

    Y2RU_ConversionParams params;
    memset(&params, 0, sizeof(params));
    params.input_format          = INPUT_YUV420_INDIV_8;
    params.output_format         = OUTPUT_RGB_16_565;
    params.rotation              = ROTATION_NONE;
    params.block_alignment       = BLOCK_LINE;  // LINEAR output, then CPU swizzle
    params.input_line_width      = (u16)width;
    params.input_lines           = (u16)height;
    params.standard_coefficient  = COEFFICIENT_ITU_R_BT_601_SCALING;
    params.alpha                 = 0xFF;

    Result rc = Y2RU_SetConversionParams(&params);
    if (R_FAILED(rc)) {
        Log("yuv_render: SetConversionParams failed 0x%08lX\n", rc);
        return false;
    }

    rc = Y2RU_SetSendingY((void *)y, (u32)(width * height), (u16)width, 0);
    if (R_FAILED(rc)) { Log("yuv_render: SetSendingY failed 0x%08lX\n", rc); return false; }

    rc = Y2RU_SetSendingU((void *)u, (u32)(width * height / 4), (u16)(width / 2), 0);
    if (R_FAILED(rc)) { Log("yuv_render: SetSendingU failed 0x%08lX\n", rc); return false; }

    rc = Y2RU_SetSendingV((void *)v, (u32)(width * height / 4), (u16)(width / 2), 0);
    if (R_FAILED(rc)) { Log("yuv_render: SetSendingV failed 0x%08lX\n", rc); return false; }

    rc = Y2RU_SetReceiving(s_temp_buf, (u32)(width * height * 2), (u16)(width * 2), 0);
    if (R_FAILED(rc)) { Log("yuv_render: SetReceiving failed 0x%08lX\n", rc); return false; }

    rc = Y2RU_StartConversion();
    if (R_FAILED(rc)) {
        Log("yuv_render: StartConversion failed 0x%08lX\n", rc);
        return false;
    }

    svcWaitSynchronization(s_done_evt, (s64)100000000LL); // 100ms max

    // Now swizzle the linear RGB565 temp buffer into the GPU texture
    Result_with_string r = Draw_set_texture_data(img, s_temp_buf,
                                                  width, height,
                                                  TEX_WIDTH, TEX_HEIGHT, GPU_RGB565);
    if (r.code != 0) {
        Log("yuv_render: Draw_set_texture_data failed: %s\n", r.string);
        return false;
    }

    return true;
}

// ---- CPU fallback (emulator or Y2R failure) ----
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
    const uint8_t *y_plane = yuv420p;
    const uint8_t *u_plane = yuv420p + (width * height);
    const uint8_t *v_plane = yuv420p + (width * height) + (width * height / 4);

    // Use temp_buf if available, otherwise stack alloc
    uint16_t *rowbuf = (uint16_t *)s_temp_buf;
    if (!rowbuf) rowbuf = (uint16_t *)alloca(width * height * 2);

    for (int row = 0; row < height; row++) {
        for (int col = 0; col < width; col++) {
            int y = y_plane[row * width + col];
            int u = u_plane[(row / 2) * (width / 2) + (col / 2)];
            int v = v_plane[(row / 2) * (width / 2) + (col / 2)];
            rowbuf[row * width + col] = yuv_to_rgb565(y, u, v);
        }
    }

    Result_with_string r = Draw_set_texture_data(img, (uint8_t *)rowbuf,
                                                  width, height,
                                                  TEX_WIDTH, TEX_HEIGHT, GPU_RGB565);
    (void)r;
}

void yuv_render_frame(Image_data *img, const uint8_t *yuv420p,
                      int width, int height) {
    if (s_y2r_ok) {
        if (!yuv_render_hw(img, yuv420p, width, height)) {
            // Y2R failed — fall back to CPU
            yuv_render_cpu(img, yuv420p, width, height);
        }
    } else {
        yuv_render_cpu(img, yuv420p, width, height);
    }
}
