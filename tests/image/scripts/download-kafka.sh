#!/bin/sh -e

FILENAME="kafka_${SCALA_VERSION}-${KAFKA_VERSION}.tgz"

url="https://dlcdn.apache.org/kafka/${KAFKA_VERSION}/${FILENAME}"

if [[ ! $(curl -s -f -I "${url}") ]]; then
    echo "Mirror does not have desired version, downloading direct from Apache"
    url="https://archive.apache.org/kafka/${KAFKA_VERSION}/${FILENAME}"
fi

echo "Downloading Kafka from $url"
wget "${url}" -O "/tmp/${FILENAME}"
