FROM ubuntu:24.04 AS builder
ARG DEBIAN_FRONTEND=noninteractive
ARG TELEGRAM_BOT_API_COMMIT=e3e9dd8e5b3d7ab8537cd5a10dc31d5ffa8f82d1
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates clang cmake git gperf libc++-dev libc++abi-dev \
    libssl-dev make zlib1g-dev && rm -rf /var/lib/apt/lists/*
WORKDIR /build
RUN git clone --recursive https://github.com/tdlib/telegram-bot-api.git source \
    && cd source \
    && git checkout "${TELEGRAM_BOT_API_COMMIT}" \
    && git submodule update --init --recursive
RUN cmake -S source -B output -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_INSTALL_PREFIX=/opt/telegram-bot-api \
    -DCMAKE_CXX_FLAGS="-stdlib=libc++" \
    && cmake --build output --target install -j2

FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates libc++1 libc++abi1 libssl3t64 zlib1g \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /opt/telegram-bot-api /opt/telegram-bot-api
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh \
    && mkdir -p /var/lib/telegram-bot-api /shared/tmp
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
