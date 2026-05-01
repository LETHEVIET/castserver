#include <3ds.h>
#include <citro2d.h>
#include <citro3d.h>
#include <arpa/inet.h>
#include <stdlib.h>
#include <malloc.h>
#include <string.h>
#include <stdio.h>

#include "config.h"
#include "draw.h"
#include "gui.h"
#include "logger.h"
#include "net/udp_rx.hpp"
#include "net/http_ctrl.hpp"
#include "yuv_render.hpp"

#define SOC_BUF_SIZE 0x100000

static void render_splash(C3D_RenderTarget *tgt, const char *msg, u32 color) {
    C3D_FrameBegin(C3D_FRAME_SYNCDRAW);
    C2D_TargetClear(tgt, C2D_Color32(0, 0, 0, 255));
    C2D_SceneBegin(tgt);
    gui_draw_text(msg, 20.0f, 110.0f, 0.5f, color);
    C3D_FrameEnd(0);
}

int main(int argc, char **argv) {
    gfxInitDefault();
    C3D_Init(C3D_DEFAULT_CMDBUF_SIZE);
    C2D_Init(C2D_DEFAULT_MAX_OBJECTS);
    C2D_Prepare();

    C3D_RenderTarget *top = C2D_CreateScreenTarget(GFX_TOP, GFX_LEFT);

    if (!gui_init()) {
        render_splash(top, "GUI init failed", GUI_RED);
        svcSleepThread(2000000000LL);
        C2D_Fini(); C3D_Fini(); gfxExit();
        return 1;
    }

    bool net_ok = false, udp_ok = false, http_ok = false;
    bool tex_ok = false, yuv_ok = false;
    char my_ip[16] = MY_IP;
    u32 *soc_buf = NULL;
    Image_data stream_img;
    memset(&stream_img, 0, sizeof(stream_img));

    // ---- Network ----
    render_splash(top, "Init: network...", GUI_WHITE);
    soc_buf = (u32 *)memalign(0x1000, SOC_BUF_SIZE);
    if (soc_buf && !R_FAILED(socInit(soc_buf, SOC_BUF_SIZE))) {
        net_ok = true;
    } else {
        free(soc_buf); soc_buf = NULL;
    }

    if (net_ok) {
        if (udp_rx_init(UDP_RECV_PORT) >= 0) udp_ok = true;
        if (http_ctrl_init(SERVER_HOST, SERVER_HTTP_PORT) >= 0) http_ok = true;
    }

    // ---- Texture ----
    render_splash(top, "Init: texture...", GUI_WHITE);
    {
        Result_with_string r = Draw_c2d_image_init(&stream_img, TEX_WIDTH, TEX_HEIGHT, GPU_RGB565);
        if (r.code == 0) {
            tex_ok = true;
            stream_img.subtex->width  = (u16)TOP_SCREEN_WIDTH;
            stream_img.subtex->height = (u16)TOP_SCREEN_HEIGHT;
            stream_img.subtex->left   = 0.0f;
            stream_img.subtex->top    = 1.0f;
            stream_img.subtex->right  = (float)TOP_SCREEN_WIDTH  / TEX_WIDTH;
            stream_img.subtex->bottom = 1.0f - (float)TOP_SCREEN_HEIGHT / TEX_HEIGHT;
            stream_img.c2d.subtex     = stream_img.subtex;
        }
    }

    // ---- YUV renderer (Y2R hw or CPU fallback) ----
    render_splash(top, "Init: YUV renderer...", GUI_WHITE);
    if (yuv_render_init() == 0) {
        yuv_ok = true;
    }

    // ---- IP detection ----
    if (net_ok) {
        u32 ip_raw = gethostid();
        if (ip_raw != 0 && ip_raw != 0xFFFFFFFF) {
            struct in_addr addr; addr.s_addr = ip_raw;
            strncpy(my_ip, inet_ntoa(addr), sizeof(my_ip) - 1);
            my_ip[sizeof(my_ip) - 1] = '\0';
        }
    }

    bool is_loopback = (strncmp(my_ip, "127.", 4) == 0);
    if (http_ok && !is_loopback) {
        http_ctrl_play(SOURCE_URL, my_ip, UDP_RECV_PORT);
    }

    uint32_t decoded_count = 0, displayed_count = 0;

    while (aptMainLoop()) {
        hidScanInput();
        u32 kDown = hidKeysDown();
        if (kDown & KEY_START) break;
        if (kDown & KEY_SELECT && http_ok) http_ctrl_keyframe();
        if (kDown & KEY_X) gui_set_overlay_visible(!gui_overlay_visible());
        if (kDown & KEY_Y) gui_set_debug_visible(!gui_debug_visible());

        bool got_frame = false;
        if (udp_ok) {
            udp_rx_drain();
            uint32_t frame_size = 0;
            const uint8_t *frame_data = udp_rx_take_frame(&frame_size);
            if (frame_data) {
                if (frame_size == YUV_FRAME_SIZE && tex_ok && yuv_ok) {
                    got_frame = true;
                    decoded_count++;
                    yuv_render_frame(&stream_img, frame_data,
                                     TOP_SCREEN_WIDTH, TOP_SCREEN_HEIGHT);
                    C3D_TexFlush(stream_img.c2d.tex);
                } else if (frame_size != YUV_FRAME_SIZE) {
                    Log("main: bad frame size %u (expected %d)\n",
                        frame_size, YUV_FRAME_SIZE);
                }
            }
        }
        if (got_frame) displayed_count++;

        char status[64];
        if (!net_ok) snprintf(status, sizeof(status), "No network");
        else if (!udp_ok) snprintf(status, sizeof(status), "UDP failed");
        else if (!http_ok) snprintf(status, sizeof(status), "HTTP failed");
        else snprintf(status, sizeof(status), "Connected | %s:%d", my_ip, UDP_RECV_PORT);

        gui_update_frame(got_frame, status, UDP_RECV_PORT);
        gui_set_udp_stats(udp_rx_recv_calls(), udp_rx_recv_bytes(),
                          udp_rx_recv_packets(), udp_rx_recv_errors());
        gui_set_frame_stats(udp_rx_nal_frames(), decoded_count, displayed_count, udp_rx_dropped_frames());

        C3D_FrameBegin(C3D_FRAME_SYNCDRAW);
        C2D_TargetClear(top, C2D_Color32(0, 0, 0, 255));
        C2D_SceneBegin(top);

        if (tex_ok) {
            Draw_texture(stream_img.c2d, 0.0f, 0.0f,
                         (float)TOP_SCREEN_WIDTH, (float)TOP_SCREEN_HEIGHT);
        }

        gui_draw();

        if (!got_frame && net_ok && udp_ok) {
            char l1[64], l2[64];
            snprintf(l1, sizeof(l1), "Waiting for stream...");
            snprintf(l2, sizeof(l2), "Server -> %s:%d", my_ip, UDP_RECV_PORT);
            gui_draw_big_status(l1, l2, GUI_WHITE);
        }

        C3D_FrameEnd(0);
    }

    if (http_ok) http_ctrl_stop();
    if (tex_ok)  Draw_c2d_image_free(stream_img);
    if (yuv_ok)  yuv_render_deinit();
    if (http_ok) http_ctrl_deinit();
    if (udp_ok)  udp_rx_deinit();
    if (net_ok)  socExit();
    free(soc_buf);

    gui_deinit();
    C2D_Fini();
    C3D_Fini();
    gfxExit();
    return 0;
}
