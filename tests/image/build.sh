podman build . --tag docker.io/kubernetesbigdataeg/kafka-alpine:3.4.0-1
podman login docker.io -u kubernetesbigdataeg
podman push docker.io/kubernetesbigdataeg/kafka-alpine:3.4.0-1