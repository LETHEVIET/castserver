#include "http_ctrl.hpp"
#include "logger.h"
#include <3ds.h>
#include <stdio.h>
#include <string.h>

static char s_host[64];
static int  s_port;

int http_ctrl_init(const char *server_host, int server_port) {
    strncpy(s_host, server_host, sizeof(s_host) - 1);
    s_host[sizeof(s_host) - 1] = '\0';
    s_port = server_port;

    Result rc = httpcInit(0);
    if (R_FAILED(rc)) {
        Log("http_ctrl: httpcInit failed: 0x%08lX\n", rc);
        return -1;
    }
    return 0;
}

void http_ctrl_deinit(void) {
    httpcExit();
}

static int post_json(const char *path, const char *json_body) {
    char url[256];
    snprintf(url, sizeof(url), "http://%s:%d%s", s_host, s_port, path);

    httpcContext ctx;
    Result rc = httpcOpenContext(&ctx, HTTPC_METHOD_POST, url, 1);
    if (R_FAILED(rc)) {
        Log("http_ctrl: open %s failed: 0x%08lX\n", path, rc);
        return -1;
    }

    httpcAddRequestHeaderField(&ctx, "Content-Type", "application/json");

    u32 body_len = (u32)strlen(json_body);
    rc = httpcAddPostDataRaw(&ctx, (const u32 *)json_body, body_len);
    if (R_FAILED(rc)) {
        Log("http_ctrl: addPostData failed: 0x%08lX\n", rc);
        httpcCloseContext(&ctx);
        return -1;
    }

    rc = httpcBeginRequest(&ctx);
    if (R_FAILED(rc)) {
        Log("http_ctrl: beginRequest failed: 0x%08lX\n", rc);
        httpcCloseContext(&ctx);
        return -1;
    }

    u32 status = 0;
    // 5-second timeout for the response
    rc = httpcGetResponseStatusCode(&ctx, &status);
    if (R_FAILED(rc)) {
        Log("http_ctrl: getStatus failed: 0x%08lX\n", rc);
        httpcCloseContext(&ctx);
        return -1;
    }

    httpcCloseContext(&ctx);
    Log("http_ctrl: POST %s → %lu\n", path, (unsigned long)status);
    return (status == 200) ? 0 : -1;
}

int http_ctrl_play(const char *source_url, const char *my_ip, int my_udp_port) {
    char body[512];
    snprintf(body, sizeof(body),
             "{\"source\":\"%s\",\"client_addr\":\"%s:%d\"}",
             source_url, my_ip, my_udp_port);
    return post_json("/play", body);
}

int http_ctrl_stop(void) {
    return post_json("/stop", "{}");
}

int http_ctrl_keyframe(void) {
    return post_json("/keyframe", "{}");
}
