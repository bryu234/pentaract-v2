#pragma once

// Runtime configuration shared by the preflight binary and the pinned TDLib patch.
#include <arpa/inet.h>
#include <cstdio>
#include <cstdlib>
#include <string>

namespace pentaract {
struct Endpoint {
  int dc_id = 0;
  int port = 0;
  std::string server;
  std::string error;

  bool enabled() const { return dc_id != 0 && error.empty(); }
};

inline std::string environment(const char *name) {
  const auto *value = std::getenv(name);
  return value == nullptr ? "" : value;
}

inline int positive_integer(const std::string &value, int maximum) {
  if (value.empty() || value.size() > 5) return 0;
  int result = 0;
  for (char digit : value) {
    if (digit < '0' || digit > '9') return 0;
    result = result * 10 + digit - '0';
  }
  return result > 0 && result <= maximum ? result : 0;
}

inline Endpoint read_endpoint() {
  Endpoint result;
  const auto dc = environment("PENTARACT_MTPROTO_DC_ID");
  result.server = environment("PENTARACT_MTPROTO_SERVER");
  const auto port = environment("PENTARACT_MTPROTO_PORT");
  if (dc.empty() && result.server.empty() && port.empty()) return result;
  result.dc_id = positive_integer(dc, 5);
  result.port = positive_integer(port, 65535);
  unsigned char address[16];
  if (!result.dc_id || !result.port ||
      (inet_pton(AF_INET, result.server.c_str(), address) != 1 &&
       inet_pton(AF_INET6, result.server.c_str(), address) != 1)) {
    result.error = "Set all PENTARACT_MTPROTO_* values: Production DC_ID (1-5), "
                   "SERVER (IPv4 or unbracketed IPv6 literal), PORT (1-65535)";
  }
  return result;
}

inline const Endpoint &endpoint() {
  static const Endpoint value = [] {
    auto result = read_endpoint();
    if (!result.error.empty()) {
      std::fprintf(stderr, "Pentaract MTProto: %s\n", result.error.c_str());
      std::exit(2);
    }
    return result;
  }();
  return value;
}

inline int initial_dc(const Endpoint &endpoint, bool is_test, int saved_dc, int default_dc) {
  if (saved_dc != 0) return saved_dc;
  return !is_test && endpoint.enabled() ? endpoint.dc_id : default_dc;
}
}  // namespace pentaract
