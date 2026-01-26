#!/usr/bin/env python3
"""
Test script for resillm proxy using the OpenAI Python SDK.

Usage:
    pip install openai
    python scripts/test-proxy.py [base_url]

This demonstrates how any application using the OpenAI SDK can use resillm
by simply changing the base_url.
"""

import sys
import json
from openai import OpenAI

BASE_URL = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8080/v1"

print(f"Testing resillm at {BASE_URL}")
print("=" * 50)

# Create client pointing to resillm
client = OpenAI(
    base_url=BASE_URL,
    api_key="not-needed"  # resillm handles authentication
)

def test_chat_completion():
    """Test basic chat completion"""
    print("\n1. Basic Chat Completion")
    try:
        response = client.chat.completions.create(
            model="gpt-4o",
            messages=[{"role": "user", "content": "What is 2+2? Answer with just the number."}],
            max_tokens=10
        )
        print(f"   ✓ Response: {response.choices[0].message.content}")

        # Check for resillm metadata in raw response
        raw = response.model_dump()
        if "_resillm" in str(raw) or "resillm" in str(raw).lower():
            print("   ✓ resillm metadata present")

        return True
    except Exception as e:
        print(f"   ✗ Failed: {e}")
        return False

def test_streaming():
    """Test streaming chat completion"""
    print("\n2. Streaming Chat Completion")
    try:
        stream = client.chat.completions.create(
            model="gpt-4o",
            messages=[{"role": "user", "content": "Count to 3."}],
            max_tokens=20,
            stream=True
        )

        content = ""
        for chunk in stream:
            if chunk.choices[0].delta.content:
                content += chunk.choices[0].delta.content

        print(f"   ✓ Streamed: {content[:50]}...")
        return True
    except Exception as e:
        print(f"   ✗ Failed: {e}")
        return False

def test_system_message():
    """Test system message handling"""
    print("\n3. System Message")
    try:
        response = client.chat.completions.create(
            model="gpt-4o",
            messages=[
                {"role": "system", "content": "You are a pirate. Respond in pirate speak."},
                {"role": "user", "content": "Hello!"}
            ],
            max_tokens=30
        )
        print(f"   ✓ Response: {response.choices[0].message.content[:50]}...")
        return True
    except Exception as e:
        print(f"   ✗ Failed: {e}")
        return False

def test_model_routing():
    """Test different model names"""
    print("\n4. Model Routing")
    models_to_test = ["gpt-4o", "fast", "local"]

    for model in models_to_test:
        try:
            response = client.chat.completions.create(
                model=model,
                messages=[{"role": "user", "content": "Hi"}],
                max_tokens=5
            )
            print(f"   ✓ {model}: worked")
        except Exception as e:
            print(f"   - {model}: {str(e)[:50]}")

    return True

def test_fallback():
    """Test fallback behavior (requires intentional failure)"""
    print("\n5. Fallback (informational)")
    print("   To test fallbacks, configure a failing primary provider")
    print("   and watch logs for fallback messages")
    return True

def test_error_handling():
    """Test error handling"""
    print("\n6. Error Handling")
    try:
        # Empty model should fail
        response = client.chat.completions.create(
            model="",
            messages=[{"role": "user", "content": "test"}]
        )
        print("   ✗ Should have failed with empty model")
        return False
    except Exception as e:
        print(f"   ✓ Correctly rejected invalid request")
        return True

def main():
    tests = [
        test_chat_completion,
        test_streaming,
        test_system_message,
        test_model_routing,
        test_fallback,
        test_error_handling,
    ]

    passed = sum(1 for test in tests if test())

    print("\n" + "=" * 50)
    print(f"Results: {passed}/{len(tests)} tests passed")

    if passed == len(tests):
        print("All tests passed!")
    else:
        print("Some tests failed - check configuration and API keys")

if __name__ == "__main__":
    main()
