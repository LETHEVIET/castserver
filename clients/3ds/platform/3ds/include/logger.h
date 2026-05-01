#pragma once
#include <string>
#include <vector>

void Log(const char *fmt, ...);
const std::vector<std::string> &GetLogs();
void ClearLogs();
