FROM mcr.microsoft.com/azurelinux/distroless/base:3.0.20260909@sha256:4377af4aa7a810b7d59f691eae5066895a71aa3eee4cfb4eba527bbebff16479
COPY controller /controller
USER 65536
ENTRYPOINT ["/controller"]
