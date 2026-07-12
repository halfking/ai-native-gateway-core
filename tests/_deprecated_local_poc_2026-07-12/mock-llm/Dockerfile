FROM python:3.12-alpine
RUN pip install --no-cache-dir flask
COPY tests/local/mock-llm/server.py /server.py
EXPOSE 8000
HEALTHCHECK --interval=10s --timeout=3s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8000/healthz || exit 1
ENTRYPOINT ["python", "/server.py"]
