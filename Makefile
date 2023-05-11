PACKAGE_NAME          := github.com/saymedia/jobsworth
GOLANG_CROSS_VERSION  ?= v1.20.4 # should match go-version in tests.yml
LIBGIT2_STATIC_BUILD_DIR ?= git2go/static-build

# -tags=static: use staticically linked git2go
# https://github.com/libgit2/git2go/tree/v34.0.0#main-branch-or-vendored-static-linking
GO_TAGS := -tags=static

# default target for development: go build without any cross building
dist/jobsworth: ${LIBGIT2_STATIC_BUILD_DIR}/install/lib/libgit2.a
	go build ${GO_TAGS} -a -o dist/ .

test: libgit2
	go test ${GO_TAGS} -a .

vet: libgit2
	go vet ${GO_TAGS}

.PHONY: libgit2
libgit2: ${LIBGIT2_STATIC_BUILD_DIR}/install/lib/libgit2.a

# to debug, add --debug
# --id=jobsworth-darwin-arm64
# This target calls back to make libgit2 from within docker
cross-build:
	@docker run \
		--rm \
		-e CGO_ENABLED=1 \
		-v `pwd`:/go/src/${PACKAGE_NAME} \
		-v `pwd`/sysroot:/sysroot \
		-w /go/src/${PACKAGE_NAME} \
		ghcr.io/goreleaser/goreleaser-cross:${GOLANG_CROSS_VERSION} \
		build --clean --snapshot \
		--debug

# based on
# https://github.com/goreleaser/goreleaser-cross-example/blob/a5a2d67e191918dbe322589d66586f67e8a66914/Makefile#L15-L25
# This target calls back to make libgit2 from within docker
.PHONY: release-dry-run
release-dry-run:
	@docker run \
		--rm \
		-e CGO_ENABLED=1 \
		-v `pwd`:/go/src/${PACKAGE_NAME} \
		-v `pwd`/sysroot:/sysroot \
		-w /go/src/${PACKAGE_NAME} \
		ghcr.io/goreleaser/goreleaser-cross:${GOLANG_CROSS_VERSION} \
		--clean --skip-validate --skip-publish --debug


# based on
# https://github.com/goreleaser/goreleaser-cross-example/blob/a5a2d67e191918dbe322589d66586f67e8a66914/Makefile#L27-L42
# This target calls back to make libgit2 from within docker
.PHONY: release
release:
	@if [ ! -f ".release-env" ]; then \
		echo "\033[91m.release-env is required for release\033[0m";\
		exit 1;\
	fi
	docker run \
		--rm \
		-e CGO_ENABLED=1 \
		--env-file .release-env \
		-v `pwd`:/go/src/${PACKAGE_NAME} \
		-v `pwd`/sysroot:/sysroot \
		-w /go/src/${PACKAGE_NAME} \
		ghcr.io/goreleaser/goreleaser-cross:${GOLANG_CROSS_VERSION} \
		release --clean


# Copy the steps from here except add USE_ICONV=OFF
# to make it dependency-free on MacOS
# https://github.com/libgit2/git2go/blob/v34.0.0/script/build-libgit2.sh#L71-L83
# verify that otool -L dist/jobsworth shows no shared libraries
# -DBUILD_TESTS=OFF: disable tests so that we don't have to install python
${LIBGIT2_STATIC_BUILD_DIR}/install/lib/libgit2.a:
	mkdir -p ${LIBGIT2_STATIC_BUILD_DIR}/build && \
	cd ${LIBGIT2_STATIC_BUILD_DIR}/build && \
	cmake -DTHREADSAFE=ON \
		-DBUILD_CLAR=OFF \
		-DBUILD_SHARED_LIBS=OFF \
		-DREGEX_BACKEND=builtin \
		-DUSE_BUNDLED_ZLIB=ON \
		-DUSE_HTTPS=OFF \
		-DUSE_SSH=OFF \
		-DCMAKE_C_FLAGS=-fPIC \
		-DCMAKE_BUILD_TYPE="RelWithDebInfo" \
		-DCMAKE_INSTALL_PREFIX="$(abspath ${LIBGIT2_STATIC_BUILD_DIR}/install)" \
		-DCMAKE_INSTALL_LIBDIR="lib" \
		-DDEPRECATE_HARD= \
		-DUSE_ICONV=OFF \
		../../vendor/libgit2
	cd ${LIBGIT2_STATIC_BUILD_DIR}/build && cmake --build . --target install

# https://pkg.go.dev/cmd/go#hdr-Build_and_test_caching
.PHONY: clean
clean:
	rm -rf ${LIBGIT2_STATIC_BUILD_DIR}/build
	rm -f dist/jobsworth
	go clean -cache # otherwise cgo doesn't know to rebuild git2go
