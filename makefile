# Makefile for etw_exporter

# name of the output binary
BINARY_NAME=etw_exporter.exe

#  build flags
# -s: Omit the symbol table
# -w: Omit the DWARF symbol table
# -gcflags="all=-B": Disable bounds checking (2-6% performance improvement)
LDFLAGS = -s -w
GCFLAGS = all=-B

# The default command to run when you just type "make"
.DEFAULT_GOAL := build

# Build the application
build:
	@echo "Building ${BINARY_NAME}..."
	go build -ldflags="${LDFLAGS}" -gcflags="${GCFLAGS}" -o ${BINARY_NAME} .

# Run the application
run:
	go run .

# Clean up the binary
clean:
	@echo "Cleaning up..."
	rm -f ${BINARY_NAME}

# Run tests
test:
	go test ./...