#!/bin/bash

# Test script for resillm proxy
# Usage: ./scripts/test-proxy.sh [base_url]

BASE_URL="${1:-http://localhost:8080}"

echo "Testing resillm at $BASE_URL"
echo "================================"

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m' # No Color

pass() { echo -e "${GREEN}✓ $1${NC}"; }
fail() { echo -e "${RED}✗ $1${NC}"; }

# Test 1: Health check
echo -e "\n1. Health Check"
HEALTH=$(curl -s "$BASE_URL/health")
if echo "$HEALTH" | grep -q "healthy"; then
    pass "Health endpoint working"
    echo "   $HEALTH"
else
    fail "Health check failed"
fi

# Test 2: Provider status
echo -e "\n2. Provider Status"
PROVIDERS=$(curl -s "$BASE_URL/v1/providers")
if echo "$PROVIDERS" | grep -q "providers"; then
    pass "Providers endpoint working"
    echo "   $PROVIDERS" | head -c 200
    echo "..."
else
    fail "Providers endpoint failed"
fi

# Test 3: Budget status
echo -e "\n3. Budget Status"
BUDGET=$(curl -s "$BASE_URL/v1/budget")
if echo "$BUDGET" | grep -q "current_hour\|enabled"; then
    pass "Budget endpoint working"
    echo "   $BUDGET" | head -c 200
    echo "..."
else
    fail "Budget endpoint failed"
fi

# Test 4: Chat completion (requires valid API keys)
echo -e "\n4. Chat Completion"
echo "   Sending test request..."
RESPONSE=$(curl -s -X POST "$BASE_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Say hello in exactly 3 words."}],
    "max_tokens": 20
  }')

if echo "$RESPONSE" | grep -q "choices"; then
    pass "Chat completion working"
    echo "   Response received with resillm metadata:"
    echo "$RESPONSE" | grep -o '"_resillm":{[^}]*}' | head -1
elif echo "$RESPONSE" | grep -q "error"; then
    fail "Chat completion failed"
    echo "   Error: $(echo "$RESPONSE" | grep -o '"message":"[^"]*"' | head -1)"
else
    fail "Unexpected response"
    echo "   $RESPONSE" | head -c 200
fi

# Test 5: Streaming (basic check)
echo -e "\n5. Streaming Chat Completion"
echo "   Sending streaming request..."
STREAM_RESPONSE=$(curl -s -X POST "$BASE_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Say hi"}],
    "stream": true,
    "max_tokens": 10
  }' 2>&1 | head -5)

if echo "$STREAM_RESPONSE" | grep -q "data:"; then
    pass "Streaming working"
    echo "   First chunk received"
else
    fail "Streaming may not be working"
    echo "   $STREAM_RESPONSE" | head -c 100
fi

# Test 6: Invalid request handling
echo -e "\n6. Error Handling"
ERROR_RESPONSE=$(curl -s -X POST "$BASE_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -d '{"model": ""}')

if echo "$ERROR_RESPONSE" | grep -q "error\|invalid"; then
    pass "Error handling working"
else
    fail "Error handling may not be working correctly"
fi

echo -e "\n================================"
echo "Tests complete!"
