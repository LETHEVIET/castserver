#include "logger.h"
#include "config.h"
#include <stdarg.h>
#include <stdio.h>

static std::vector<std::string> g_logs;

void Log(const char *fmt, ...) {
  char buffer[256];
  va_list args;
  va_start(args, fmt);
  vsnprintf(buffer, sizeof(buffer), fmt, args);
  va_end(args);

  printf("%s", buffer);

  g_logs.push_back(std::string(buffer));
  if (g_logs.size() > MAX_LOG_LINES) {
    g_logs.erase(g_logs.begin());
  }
}

const std::vector<std::string> &GetLogs() { return g_logs; }

void ClearLogs() { g_logs.clear(); }
