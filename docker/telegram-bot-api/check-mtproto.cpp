#include "PentaractEndpoint.h"

int main() {
  const auto config = pentaract::read_endpoint();
  if (!config.error.empty()) {
    std::fprintf(stderr, "Pentaract MTProto: %s\n", config.error.c_str());
    return 2;
  }
  if (config.enabled()) {
    std::fprintf(stderr, "Pentaract MTProto: Production DC%d preferred endpoint [%s]:%d; "
                         "standard TDLib fallback enabled\n",
                 config.dc_id, config.server.c_str(), config.port);
  } else {
    std::fprintf(stderr, "Pentaract MTProto: standard TDLib endpoints\n");
  }
}
