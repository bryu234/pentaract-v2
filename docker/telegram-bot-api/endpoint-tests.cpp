#include "td/telegram/net/PentaractDcOptions.h"

#include <cstdlib>
#include <iostream>

void require(bool value, const char *message) {
  if (!value) {
    std::cerr << "FAIL: " << message << '\n';
    std::exit(1);
  }
}

void config(const char *dc, const char *ip, const char *port) {
  setenv("PENTARACT_MTPROTO_DC_ID", dc, 1);
  setenv("PENTARACT_MTPROTO_SERVER", ip, 1);
  setenv("PENTARACT_MTPROTO_PORT", port, 1);
}

td::DcOptions candidates(int dc, const char *ip) {
  td::IPAddress address;
  address.init_host_port(td::string(ip), 443).ensure();
  td::DcOptions result;
  result.dc_options.emplace_back(td::DcId::internal(dc), address);
  return result;
}

td::DcOptionsSet::ConnectionInfo select(td::DcOptionsSet &options, int dc = 2) {
  auto result = options.find_connection(td::DcId::internal(dc), false, false, false, false);
  require(result.is_ok(), "connection selection succeeds");
  return result.move_as_ok();
}

bool address_is(const td::DcOptionsSet::ConnectionInfo &info, const char *ip) {
  return info.option->get_ip_address().get_ip_str().str() == ip;
}

int main() {
  config("", "", "");
  require(!pentaract::read_endpoint().enabled() && pentaract::read_endpoint().error.empty(), "unset is compatible");
  config("2", "192.0.2.2", "443");
  const auto endpoint = pentaract::read_endpoint();
  require(endpoint.enabled(), "valid IPv4 endpoint");
  config("2", "2001:db8::2", "65535");
  require(pentaract::read_endpoint().enabled(), "valid IPv6 endpoint");
  for (const auto *bad : {"", "0", "6", "-1", "2x", "9999999999999999999"}) {
    config(bad, "192.0.2.2", "443");
    require(!pentaract::read_endpoint().error.empty(), "reject invalid DC");
  }
  for (const auto *bad : {"", "0", "65536", "-1", "443\n", "443;id"}) {
    config("2", "192.0.2.2", bad);
    require(!pentaract::read_endpoint().error.empty(), "reject invalid port");
  }
  for (const auto *bad : {"", "api.telegram.org", "http://192.0.2.2", "192.0.2.999", "[::1]", "$(id)"}) {
    config("2", bad, "443");
    require(!pentaract::read_endpoint().error.empty(), "reject nonliteral IP");
  }
  require(pentaract::initial_dc(endpoint, false, 0, 1) == 2, "new session starts at configured DC");
  require(pentaract::initial_dc(endpoint, false, 4, 1) == 4, "preserve saved/migrated DC");
  require(pentaract::initial_dc(endpoint, true, 0, 1) == 1, "never route Test to Production endpoint");
  require(pentaract::initial_dc({}, false, 0, 1) == 1, "unset preserves default DC");

  td::DcOptionsSet options;
  options.add_dc_options(candidates(2, "192.0.2.1"));
  pentaract::add_preferred(options, {}, false);
  require(address_is(select(options), "192.0.2.1"), "unset preserves routing");
  pentaract::add_preferred(options, endpoint, true);
  require(address_is(select(options), "192.0.2.1"), "Test ignores Production endpoint");
  pentaract::add_preferred(options, endpoint, false);
  require(address_is(select(options), "192.0.2.2"), "configured endpoint takes priority");
  auto preferred = select(options);
  preferred.stat->on_error();
  require(address_is(select(options), "192.0.2.1"), "failed endpoint falls back");

  options.reset();
  options.add_dc_options(candidates(2, "192.0.2.3"));
  options.add_dc_options(candidates(4, "192.0.2.4"));
  pentaract::add_preferred(options, endpoint, false);
  auto all = options.find_all_connections(td::DcId::internal(2), false, false, false, false);
  bool found = false;
  for (auto &info : all) {
    if (address_is(info, "192.0.2.2")) {
      found = true;
      // Simulate an authenticated successful connection after its earlier error.
      info.stat->error_at = -1001;
      info.stat->on_ok();
    }
  }
  require(found, "preferred endpoint survives server configuration refresh");
  require(address_is(select(options), "192.0.2.2"), "recovered endpoint retains priority");
  require(address_is(select(options, 4), "192.0.2.4"), "other DC routing is preserved");
  std::cout << "MTProto configuration and TDLib routing tests passed\n";
}
