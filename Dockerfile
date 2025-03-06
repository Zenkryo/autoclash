FROM debian:latest

# 设置工作目录
WORKDIR /app

# 复制可执行文件和配置文件到容器中
COPY autoclash /app/autoclash
COPY config.yml /app/config.yml

# 给可执行文件添加执行权限
RUN chmod +x /app/autoclash

# 指定容器启动时运行的命令
ENTRYPOINT ["/app/autoclash"]
