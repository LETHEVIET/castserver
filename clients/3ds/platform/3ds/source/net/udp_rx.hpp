#pragma once
#include <stdint.h>
#include <stdbool.h>

// Packet header: magic(4) + frame_id(4) + chunk_id(2) + total_chunks(2) + width(2) + height(2)
#define UDP_MAGIC       0x33445331u
#define UDP_HDR_SIZE    16
#define UDP_MAX_PAYLOAD 1400
#define UDP_MAX_DGRAM   (UDP_HDR_SIZE + UDP_MAX_PAYLOAD)

// Open non-blocking UDP socket bound to port. Returns 0 on success.
int udp_rx_init(int port);
void udp_rx_deinit(void);

// Consume all pending datagrams and update the reassembly state.
// Returns number of complete frames that became available this call.
int udp_rx_drain(void);

// Returns pointer + size of the next complete frame, or NULL if none.
// Pointer is valid only until the next udp_rx_drain() call.
const uint8_t *udp_rx_take_frame(uint32_t *out_size);

// Get the dimensions of the current stream (read from UDP headers).
// Valid after at least one packet has been received.
void udp_rx_get_dimensions(uint16_t *out_width, uint16_t *out_height);

// Diagnostic counters (for debug panel)
uint32_t udp_rx_recv_calls(void);
uint32_t udp_rx_recv_bytes(void);
uint32_t udp_rx_recv_packets(void);
uint32_t udp_rx_recv_errors(void);
uint32_t udp_rx_nal_frames(void);
uint32_t udp_rx_dropped_frames(void);