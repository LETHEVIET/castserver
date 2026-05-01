#include "y2r_upload.hpp"
#include "config.h"
#include "logger.h"
#include <3ds.h>
#include <string.h>

// NV12 UV is interleaved; Y2R INDIV_8 wants separate planar U and V.
// Planar dimensions for 400×240: U = 200×120, V = 200×120.
#define UV_PLANE_SIZE (200 * 120)

static uint8_t *s_u_plane   = NULL;
static uint8_t *s_v_plane   = NULL;
static Handle   s_done_evt;
static bool     s_inited    = false;

int y2r_init(void) {
    if (s_inited) return 0;

    s_u_plane = (uint8_t *)linearMemAlign(UV_PLANE_SIZE, 0x80);
    s_v_plane = (uint8_t *)linearMemAlign(UV_PLANE_SIZE, 0x80);
    if (!s_u_plane || !s_v_plane) {
        Log("y2r: linearAlloc U/V planes failed\n");
        return -1;
    }

    Result rc = y2rInit();
    if (R_FAILED(rc)) {
        Log("y2r: y2rInit failed: 0x%08lX\n", rc);
        linearFree(s_u_plane);
        linearFree(s_v_plane);
        s_u_plane = s_v_plane = NULL;
        return -1;
    }

    rc = Y2RU_GetTransferEndEvent(&s_done_evt);
    if (R_FAILED(rc)) {
        Log("y2r: GetTransferEndEvent failed: 0x%08lX\n", rc);
        y2rExit();
        linearFree(s_u_plane);
        linearFree(s_v_plane);
        s_u_plane = s_v_plane = NULL;
        return -1;
    }

    s_inited = true;
    Log("y2r: initialized (NV12 → BLOCK_8_BY_8 RGB565)\n");
    return 0;
}

void y2r_deinit(void) {
    if (!s_inited) return;
    Y2RU_StopConversion();
    y2rExit();
    if (s_u_plane) { linearFree(s_u_plane); s_u_plane = NULL; }
    if (s_v_plane) { linearFree(s_v_plane); s_v_plane = NULL; }
    s_inited = false;
}

// Split interleaved NV12 UV (U0V0 U1V1 ...) into separate planar buffers.
static void deinterleave_uv(const uint8_t *nv12_uv,
                              uint8_t *u_out, uint8_t *v_out) {
    for (int i = 0; i < UV_PLANE_SIZE; i++) {
        u_out[i] = nv12_uv[i * 2];
        v_out[i] = nv12_uv[i * 2 + 1];
    }
}

int y2r_upload(const MVD_Frame *frame, void *tex_data) {
    if (!s_inited) return -1;

    deinterleave_uv(frame->uv, s_u_plane, s_v_plane);

    Y2RU_StopConversion();

    Y2RU_ConversionParams params;
    params.input_format          = INPUT_YUV420_INDIV_8;
    params.output_format         = OUTPUT_RGB_16_565;
    params.rotation              = ROTATION_NONE;
    params.block_alignment       = BLOCK_8_BY_8;
    params.input_line_width      = 400;
    params.input_lines           = 240;
    params.standard_coefficient  = COEFFICIENT_ITU_R_BT_601_SCALING;
    params.unused                = 0;
    params.alpha                 = 0xFF;

    Result rc = Y2RU_SetConversionParams(&params);
    if (R_FAILED(rc)) {
        Log("y2r: SetConversionParams failed: 0x%08lX\n", rc);
        return -1;
    }

    // Input planes — all in linear memory (allocated in mvd_h264.cpp / y2r_init)
    Y2RU_SetSendingY(frame->y,  400 * 240,  400, 0);
    Y2RU_SetSendingU(s_u_plane, UV_PLANE_SIZE, 200, 0);
    Y2RU_SetSendingV(s_v_plane, UV_PLANE_SIZE, 200, 0);

    // Output — GPU-tiled RGB565 into the 512×256 texture atlas.
    // Each DMA unit = 8 rows of 400 pixels = 6400 bytes of actual content.
    // After each unit we skip (512-400)*8*2 = 1792 bytes (unused atlas columns).
    // Total buffer: TEX_WIDTH * TEX_HEIGHT * sizeof(u16) = 512*256*2 = 262144 bytes.
    const u16 unit_bytes   = 400 * 8 * 2;
    const s16 output_gap   = (TEX_WIDTH - 400) * 8 * 2;
    const u32 tex_buf_size = TEX_WIDTH * TEX_HEIGHT * 2;

    Y2RU_SetReceiving(tex_data, tex_buf_size, unit_bytes, output_gap);

    rc = Y2RU_StartConversion();
    if (R_FAILED(rc)) {
        Log("y2r: StartConversion failed: 0x%08lX\n", rc);
        return -1;
    }

    // Wait up to 1 s for the DMA to complete
    svcWaitSynchronization(s_done_evt, (s64)1000000000LL);
    return 0;
}
