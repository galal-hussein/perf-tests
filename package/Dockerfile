FROM ubuntu:latest
COPY bin/clusterloader /usr/bin/
COPY clusterloader2/pkg/prometheus/manifests /opt/manifests/
COPY clusterloader2/pkg/measurement/common/probes/manifests/ /opt/probes-manifests

CMD ["clusterloader"]
