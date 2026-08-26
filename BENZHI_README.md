基于 Go 实现的近海漂流浮标搜寻协同 Web 项目，一款后端服务，提供漂移扇区推演、船只调度、执行协同与任务复盘。

# offshore-buoy-drift-search-loop

本 Git 项目来自模型完成任务后的 workspace，不包含嵌套 .git 记录或本地构建产物。

## 本地构建与测试

```bash
go mod download
go build ./...
go test ./...
./run_benzhi_smoke.sh
```

## Docker 构建与运行

```bash
docker build --platform linux/amd64 -t offshore-buoy-drift-search-loop:latest .
./build_benzhi_docker.sh offshore-buoy-drift-search-loop linux/arm64
docker run --rm -it --platform linux/arm64 offshore-buoy-drift-search-loop:latest
./build_benzhi_docker.sh offshore-buoy-drift-search-loop linux/amd64
docker run --rm -it --platform linux/amd64 offshore-buoy-drift-search-loop:latest
```

构建脚本第二个参数为目标平台，必须分别完成 linux/arm64 和 linux/amd64 构建与容器验证；未提供时按照规范默认使用 linux/amd64。系统 backend-v2 模板通过 Go 原生交叉编译生成目标架构的 /usr/local/bin/benzhi-app，镜像默认直接运行该入口。
