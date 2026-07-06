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
RUN apt-get update && apt-get install -y --no-install-recommends \
        git cmake ninja-build gcc g++ patchelf ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /build
RUN git clone --depth 1 https://github.com/edgardmessias/opus_mlow.git
WORKDIR /build/opus_mlow
RUN cmake -B build -G Ninja -DBUILD_SHARED_LIBS=ON -DCMAKE_BUILD_TYPE=Release \
        -DOPUS_BUILD_PROGRAMS=OFF -DOPUS_BUILD_TESTING=OFF \
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
EXPOSE 8080 50000
ENTRYPOINT ["wacalls"]
CMD ["-addr", ":8080", "-static", "/app/client/dist", "-db", "/data/wacalls.db"]
