.PHONY: build clean run test list

build:
	@echo "Building main transporter binary..."
	go build -o transporter ./cmd/transporter
	@echo "Main transporter binary built successfully"

install-deps:
	@echo "Installing dependencies..." 
	go mod init transporter
	go get k8s.io/apimachinery k8s.io/client-go sigs.k8s.io/controller-runtime

generate-crd:
	@echo "Generating CRD..."
	mkdir -p helm-chart/crds
	~/go/bin/controller-gen crd:allowDangerousTypes=true paths=./api/v1alpha1/... output:stdout > ./helm-chart/crds/migration.transporter.io_podmigrations.yaml
	@echo "CRD generated successfully"

generate-proto:
	@echo "Generating proto..."
	protoc --plugin=protoc-gen-go=$HOME/go/bin/protoc-gen-go \
		--plugin=protoc-gen-go-grpc=$HOME/go/bin/protoc-gen-go-grpc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		pkg/agent/api/migration.proto
	@echo "Proto generated successfully"

clean:
	rm -f transporter
	rm -rf dist/
	
run: build
	@echo "Running transporter..."
	./transporter

test:
	@echo "Building and testing transporter..."
	./transporter migrate test-pod -n default -t node-01
	./transporter status mig-abc123

# Create distribution package
dist: build
	@mkdir -p dist
	@cp transporter dist/
	@echo "Distribution created in ./dist/"

list:
	./transporter list