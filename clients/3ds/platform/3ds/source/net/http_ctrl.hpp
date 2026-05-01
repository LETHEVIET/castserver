#pragma once

// Initialize the httpc subsystem and store server coordinates.
int http_ctrl_init(const char *server_host, int server_port);
void http_ctrl_deinit(void);

// POST /play  — tell server to start streaming to my_ip:my_udp_port
int http_ctrl_play(const char *source_url, const char *my_ip, int my_udp_port);

// POST /stop
int http_ctrl_stop(void);

// POST /keyframe — request an IDR frame on the next encode
int http_ctrl_keyframe(void);
