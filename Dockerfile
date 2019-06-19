FROM alpine:3.8

COPY ./build/bin/k8s-pod-headless-service-operator /bin/k8s-pod-headless-service-operator

ENTRYPOINT ["/bin/k8s-pod-headless-service-operator"]
CMD [ "run" ]
