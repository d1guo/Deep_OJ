#!/bin/bash
exec > check_env.log 2>&1

echo "=== Compiler Check ==="
which g++ && g++ --version | head -n 1 || echo "g++ missing"
which gcc && gcc --version | head -n 1 || echo "gcc missing"
which cmake && cmake --version | head -n 1 || echo "cmake missing"
which make && make --version | head -n 1 || echo "make missing"
which pkg-config && pkg-config --version || echo "pkg-config missing"

echo -e "\n=== Go Check ==="
which go && go version || echo "go missing"

echo -e "\n=== Protobuf Check ==="
which protoc && protoc --version || echo "protoc missing"
which grpc_cpp_plugin || echo "grpc_cpp_plugin missing"

echo -e "\n=== Library Check (via pkg-config) ==="
libs=(grpc++ protobuf libseccomp yaml-cpp libcurl openssl hiredis redis++)
for lib in "${libs[@]}"; do
    if pkg-config --exists "$lib"; then
        echo "$lib found ($(pkg-config --modversion "$lib"))"
    else
        echo "$lib missing"
    fi
done

echo -e "\n=== Docker Check ==="
which docker && docker --version || echo "docker missing"
(docker-compose version 2>/dev/null && echo "docker-compose found") || (docker compose version 2>/dev/null && echo "docker compose found") || echo "docker-compose missing"

echo -e "\n=== Running Services Check ==="
docker ps --format "table {{.Names}}\t{{.Status}}" || echo "Unable to list docker services"
