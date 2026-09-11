#pragma once

#include "td/telegram/net/PentaractEndpoint.h"
#include "td/telegram/net/DcOptionsSet.h"

namespace pentaract {
// add_dc_options prepends candidates; call AFTER defaults and cached/server options.
// Keep upstream health ranking, media routing and other DCs intact.
inline void add_preferred(td::DcOptionsSet &options, const Endpoint &endpoint, bool is_test) {
  if (is_test || !endpoint.enabled()) return;
  td::IPAddress address;
  address.init_host_port(endpoint.server, endpoint.port).ensure();
  td::DcOptions preferred;
  preferred.dc_options.emplace_back(td::DcId::internal(endpoint.dc_id), address);
  options.add_dc_options(std::move(preferred));
}
}  // namespace pentaract
