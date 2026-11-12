FROM python:3.12-slim
WORKDIR /app
COPY sidecar.py .
USER nobody
EXPOSE 8090
CMD ["python", "-u", "sidecar.py"]
