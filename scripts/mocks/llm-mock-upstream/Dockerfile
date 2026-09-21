FROM python:3.11-slim

WORKDIR /app

# Install dependencies
RUN pip install --no-cache-dir aiohttp

# Copy the mock server
COPY server-v2.py /app/

# Expose ports
EXPOSE 18080

# Run the server
ENV MOCK_PORT=18080
ENV MOCK_TOKEN=mock-provider-01
CMD ["python", "server-v2.py"]
