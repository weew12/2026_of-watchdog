# =========================
# Config (可自定义)
# =========================
IMG_NAME ?= of-watchdog
OWNER    ?= weew12
SERVER   ?= 172.16.2.106:5000
# TAG      ?= $(GIT_VERSION)
TAG      ?= 0.11.4

BUILDER_NAME ?= multiarch
NODE_NAME    ?= $(BUILDER_NAME)
# PLATFORMS    ?= linux/amd64,linux/arm/v7,linux/arm64
PLATFORMS    ?= linux/amd64,linux/arm64
BUILDKIT_CONFIG ?= ./docker_buildx_config/buildkitd.toml

# =========================
# Git info
# =========================
GIT_COMMIT := $(shell git rev-parse HEAD)
GIT_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null)
GIT_UNTRACKEDCHANGES := $(shell git status --porcelain --untracked-files=no)
ifneq ($(GIT_UNTRACKEDCHANGES),)
	GIT_VERSION := $(GIT_VERSION)-$(shell date +"%s")
endif

# =========================
# Colors
# =========================
RESET   := \033[0m
BOLD    := \033[1m
GREEN   := \033[32m
BLUE    := \033[34m
CYAN    := \033[36m
MAGENTA := \033[35m
YELLOW  := \033[33m

export DOCKER_CLI_EXPERIMENTAL=enabled
export DOCKER_BUILDKIT=1

# =========================
# Buildx preparation
# =========================
.PHONY: buildx-prepare
buildx-prepare:
	@printf "$(BLUE)$(BOLD)==> step 1/4: install qemu/binfmt$(RESET)\n"
	@docker run --privileged --rm tonistiigi/binfmt --install all
	@printf "$(BLUE)$(BOLD)==> step 2/4: remove old builder if exists$(RESET)\n"
	@docker buildx rm $(BUILDER_NAME) 2>/dev/null || true
	@printf "$(BLUE)$(BOLD)==> step 3/4: create buildx builder$(RESET) $(CYAN)$(BUILDER_NAME)$(RESET)\n"
	@docker buildx create \
		--name $(BUILDER_NAME) \
		--node $(NODE_NAME) \
		--driver docker-container \
		--buildkitd-config $(BUILDKIT_CONFIG) \
		--use
	@printf "$(BLUE)$(BOLD)==> step 4/4: bootstrap builder$(RESET)\n"
	@docker buildx inspect $(BUILDER_NAME) --bootstrap
	@printf "$(GREEN)$(BOLD)==> builder ready$(RESET)\n"

# =========================
# Publish multi-arch image (可自定义 SERVER/OWNER/IMG_NAME/TAG)
# =========================
.PHONY: publish-buildx-all
publish-buildx-all: buildx-prepare
	@printf "$(MAGENTA)$(BOLD)==> publish image$(RESET)\n"
	@printf "$(YELLOW)    image: $(CYAN)$(SERVER)/$(OWNER)/$(IMG_NAME):$(TAG)$(RESET)\n"
	@printf "$(YELLOW)    platforms: $(CYAN)$(PLATFORMS)$(RESET)\n"
	@printf "$(YELLOW)    git commit: $(CYAN)$(GIT_COMMIT)$(RESET)\n"
	@printf "$(YELLOW)    version: $(CYAN)$(GIT_VERSION)$(RESET)\n"
	@docker buildx build \
		--builder $(BUILDER_NAME) \
		--platform $(PLATFORMS) \
		--push \
		--build-arg VERSION=$(GIT_VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--tag $(SERVER)/$(OWNER)/$(IMG_NAME):$(TAG) \
		.
	@printf "$(GREEN)$(BOLD)==> publish done$(RESET)\n"

# =========================
# 使用示例
# =========================
# 可以这样自定义变量：
# make publish-buildx-all SERVER=docker.io OWNER=alexellis TAG=ready