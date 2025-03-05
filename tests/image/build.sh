podman build . --tag docker.io/kubernetesbigdataeg/kafka:3.9.0-1
podman login docker.io -u kubernetesbigdataeg
podman push docker.io/kubernetesbigdataeg/kafka:3.9.0-1