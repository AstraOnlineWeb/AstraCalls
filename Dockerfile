# syntax=docker/dockerfile:1

# ---------- Stage 1: build do client React ----------
FROM node:22-bookworm AS client
WORKDIR /app/client
COPY client/package*.json ./
RUN npm ci
COPY client/ ./
RUN npm run build

# ---------- Stage 2: compila o codec MLow (libopus_mlow.so) ----------
FROM debian:bookworm AS opus
# TARGETARCH é injetado automaticamente pelo buildx (amd64 | arm64). Num `docker
# build` comum (sem buildx) ele vem vazio -> usamos `uname -m` como fallback.
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends \
        git cmake ninja-build gcc g++ patchelf ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /build
RUN git clone --depth 1 https://github.com/edgardmessias/opus_mlow.git
WORKDIR /build/opus_mlow
# PORTABILIDADE DO SIMD: o fork força "-mavx" nos fontes do MLow (smpl_*), sem
# detecção de CPU em runtime. Os smpl_*.c são C puro (zero intrínsecos), então
# ajustamos o flag por arquitetura:
#  - x86-64: baixamos p/ baseline SSE2. Sem isso a lib sai com AVX embutido e
#            QUEBRA (SIGILL/SIGSEGV) em CPUs/VPS sem AVX. Roda em qualquer x86-64.
#  - arm64 : "-mavx"/"-msse2" nem existem no gcc ARM; trocamos por "-O2" (NEON é
#            baseline no ARMv8, não precisa de flag) -> compila e roda nativo.
RUN if [ "$TARGETARCH" = "arm64" ] || [ "$(uname -m)" = "aarch64" ]; then \
        sed -i 's/COMPILE_FLAGS -mavx/COMPILE_FLAGS -O2/' CMakeLists.txt; \
    else \
        sed -i 's/COMPILE_FLAGS -mavx/COMPILE_FLAGS -msse2/' CMakeLists.txt; \
    fi
# As opções OPUS_X86_PRESUME_* só existem no ramo x86 do cmake do opus; em arm64
# são inofensivas (variáveis não usadas), então mantê-las não quebra o build.
RUN cmake -B build -G Ninja -DBUILD_SHARED_LIBS=ON -DCMAKE_BUILD_TYPE=Release \
        -DOPUS_BUILD_PROGRAMS=OFF -DOPUS_BUILD_TESTING=OFF \
        -DOPUS_X86_PRESUME_AVX=OFF -DOPUS_X86_PRESUME_AVX2=OFF \
    && cmake --build build \
    && cp "$(readlink -f build/libopus.so)" /opt/libopus_mlow.so \
    && patchelf --set-soname libopus_mlow.so /opt/libopus_mlow.so

# ---------- Stage 3: build do servidor Go (cgo + tag mlow) ----------
FROM golang:1.26-bookworm AS server
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev zip \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# empacota a extensão de passkey p/ download pelo painel (ver /astracalls-passkey.zip)
RUN cd /src && zip -r -q /astracalls-passkey.zip passkey-extension -x '*.DS_Store'
COPY --from=opus /opt/libopus_mlow.so /src/native/libopus_mlow.so
ENV CGO_ENABLED=1 \
    CC=gcc \
    CGO_LDFLAGS="-L/src/native -Wl,-rpath,/usr/local/lib"
RUN go build -tags mlow -o /wacalls ./cmd/server

# ---------- Stage 4: runtime enxuto ----------
FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates ffmpeg \
    && rm -rf /var/lib/apt/lists/*
COPY --from=opus /opt/libopus_mlow.so /usr/local/lib/libopus_mlow.so
RUN ldconfig
COPY --from=server /wacalls /usr/local/bin/wacalls
COPY --from=client /app/client/dist /app/client/dist
COPY --from=server /astracalls-passkey.zip /app/client/dist/astracalls-passkey.zip
WORKDIR /app
# Revisão do build (commit + timestamp): identifica a imagem e garante digest
# único a cada build de release. Injetado via --build-arg BUILD_REV=...
ARG BUILD_REV=dev
LABEL org.opencontainers.image.revision=$BUILD_REV
EXPOSE 8080 50000
ENTRYPOINT ["wacalls"]
CMD ["-addr", ":8080", "-static", "/app/client/dist", "-db", "/data/wacalls.db"]
