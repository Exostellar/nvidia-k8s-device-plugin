import json
import random
import time
import requests
from threading import Thread

requests_per_minute_A = 25
requests_per_minute_B = 25

jobs = [
#    {"url": "http://localhost:8001/v1/completions", "interval": 1/requests_per_minute_A*60, "job_name": "LLM #A"},
    {"url": "http://localhost:8002/v1/completions", "interval": 1/requests_per_minute_B*60, "job_name": "LLM #B"},
]

# Shared data structure to track results per job
job_results = {}

def monitor_all_jobs(job_results):
    """
    Monitors all jobs and prints both per-job and aggregate requests per minute,
    along with the prompt and response.
    """
    while True:
        time.sleep(60)  # Aggregate over one minute
        total_requests = 0

        # Lock-free since all threads write to separate lists
        for job_name, results in job_results.items():
            requests_handled = len(results)
            print(f"[{job_name}] Requests per minute: {requests_handled}")
            
            # Print the prompt and response for each request
            for elapsed_time, prompt, response in results[:5]:
                print(f"   ?  {prompt}")
                print(f'   =  {response[:40]}...')
            
            print(f"   ... {max(0, len(results)-5) } more requests\n")
            
            total_requests += requests_handled
            results.clear()

        print(f"[Aggregate] Requests per minute: {total_requests:.2f}")

prompts = [
    "Once upon a time,",
    "In a galaxy far away,",
    "The quick brown fox,",
    "To be or not to be,",
    "There was light,",
    "It was a cold day,",
    "A village in the mountains,",
    "Beneath the deep ocean,",
    "At midnight, a figure appeared,",
    "A hidden forest path,",
    "Where dragons still roam,",
    "The wind howled outside,",
    "Two travelers shared tales,",
    "A secret meeting began,",
    "A treasure map in hand,",
    "Snow fell softly,",
    "The sky turned red,",
    "A shadow moved quickly,",
    "On the edge of space,",
    "The ship set sail,",
    "Whispers filled the air,",
    "The child looked up,",
    "Stars twinkled above,",
    "A fire burned bright,",
    "The knight drew his sword,",
    "A door creaked open,",
    "The castle stood empty,",
    "The wolves began to howl,",
    "A key was found,",
    "The storm raged on,"
]


def send_request(url, headers, data, interval, results):
    """
    Sends HTTP requests at a given interval and records the response times,
    along with the prompt and response content.
    """
    while True:
        data["prompt"] = random.choice(prompts)
        start_time = time.perf_counter()
        prompt = data["prompt"]
        try:
            response = requests.post(url, headers=headers, json=data)
            response_text = response.text
            response_text = json.loads(response_text)
            response_text = response_text["choices"][0]["text"]
            response_text = response_text.replace("\n", " ")
            end_time = time.perf_counter()
            elapsed_time = end_time - start_time
            results.append((elapsed_time, prompt, response_text))
        except Exception as e:
            print(f"Request failed: {e}")
            elapsed_time = time.perf_counter() - start_time
            results.append((elapsed_time, prompt, "ERROR"))

        remaining_time = interval - elapsed_time
        if remaining_time > 0:
            time.sleep(remaining_time)

def job(url, headers, data, interval, job_name, job_results):
    """
    Executes a single job to send requests.
    """
    results = []
    job_results[job_name] = results
    send_request(url, headers, data, interval, results)

def main():
    """
    Starts multiple jobs and the single monitor thread.
    """
    headers = {
        "accept": "application/json",
        "Content-Type": "application/json"
    }
    data = {
        "model": "meta/llama-3.1-8b-instruct",
        "max_tokens": 80
    }

    # Start all jobs
    threads = [
        Thread(target=job, args=(job_data["url"], headers, data, job_data["interval"], job_data["job_name"], job_results))
        for job_data in jobs
    ]

    # Start the monitor thread
    monitor_thread = Thread(target=monitor_all_jobs, args=(job_results,))
    monitor_thread.start()

    for thread in threads:
        thread.start()

    for thread in threads:
        thread.join()

    # Join the monitor thread (this will run indefinitely)
    monitor_thread.join()

if __name__ == "__main__":
    main()
