#include "mvd_h264.hpp"
#include "logger.h"
#include <3ds.h>
#include <string.h>

#define VIDEO_WIDTH  400
#define VIDEO_HEIGHT 240
#define OUTPUT_BUF_SIZE (VIDEO_WIDTH * VIDEO_HEIGHT * 2)

static uint8_t *s_out_buf    = NULL;
static uint8_t *s_input_buf  = NULL;
static uint32_t s_input_cap  = 0;
static bool     s_inited     = false;

int mvd_h264_init(void) {
    if (s_inited) return 0;

    s_out_buf = (uint8_t *)linearMemAlign(OUTPUT_BUF_SIZE, 0x80);
    if (!s_out_buf) {
        Log("mvd: linearAlloc output buffer failed\n");
        return -1;
    }

    // Try 15 MB → 5 MB, same as FourthTube
    Result rc = -1;
    for (int mb = 15; mb >= 5; mb--) {
        rc = mvdstdInit(MVDMODE_VIDEOPROCESSING,
                        MVD_INPUT_H264,
                        MVD_OUTPUT_BGR565,
                        mb * 1000 * 1000,
                        NULL);
        if (!R_FAILED(rc)) {
            Log("mvd: mvdstdInit ok with %d MB\n", mb);
            break;
        }
    }
    if (R_FAILED(rc)) {
        Log("mvd: mvdstdInit failed: 0x%08lX\n", rc);
        linearFree(s_out_buf);
        s_out_buf = NULL;
        return -1;
    }

    s_inited = true;
    Log("mvd: H.264 decoder ready\n");
    return 0;
}

void mvd_h264_deinit(void) {
    if (!s_inited) return;
    mvdstdExit();
    if (s_out_buf)   { linearFree(s_out_buf);   s_out_buf   = NULL; }
    if (s_input_buf) { linearFree(s_input_buf); s_input_buf = NULL; s_input_cap = 0; }
    s_inited = false;
}

int mvd_h264_decode(const uint8_t *annex_b, uint32_t size, MVD_Frame *out) {
    if (!s_inited) return -1;

    if (size > s_input_cap) {
        if (s_input_buf) linearFree(s_input_buf);
        s_input_cap = (size + 0xFFF) & ~0xFFFu;
        s_input_buf = (uint8_t *)linearMemAlign(s_input_cap, 0x80);
        if (!s_input_buf) {
            s_input_cap = 0;
            Log("mvd: input alloc failed (%lu B)\n", (unsigned long)size);
            return -1;
        }
    }

    memcpy(s_input_buf, annex_b, size);

    Result rc = mvdstdProcessVideoFrame(s_input_buf, (size_t)size, 0, NULL);
    if (!MVD_CHECKNALUPROC_SUCCESS(rc)) {
        Log("mvd: ProcessVideoFrame failed: 0x%08lX\n", rc);
        return -1;
    }

    if (rc == MVD_STATUS_FRAMEREADY) {
        // Generate config per-frame (FourthTube / ThirdTube pattern)
        MVDSTD_Config config;
        mvdstdGenerateDefaultConfig(&config,
                                    VIDEO_WIDTH, VIDEO_HEIGHT,
                                    VIDEO_WIDTH, VIDEO_HEIGHT,
                                    NULL, NULL, NULL);
        config.physaddr_outdata0 = osConvertVirtToPhys(s_out_buf);
        config.physaddr_outdata1 = osConvertVirtToPhys(s_out_buf);

        rc = mvdstdRenderVideoFrame(&config, true);
        if (R_FAILED(rc)) {
            Log("mvd: RenderVideoFrame failed: 0x%08lX\n", rc);
            return -1;
        }
        out->rgb565 = s_out_buf;
        out->size   = OUTPUT_BUF_SIZE;
        out->width  = VIDEO_WIDTH;
        out->height = VIDEO_HEIGHT;
        return 0;
    }

    return 1; // need more input
}
