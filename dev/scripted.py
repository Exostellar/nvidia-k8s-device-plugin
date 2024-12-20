import json
import random
import time
import requests
from threading import Thread, Event

# Shared data structure to track results per job
job_results = {}
stop_event = Event()  # Event to signal stopping the threads

prompts = [
    "Once upon a time,", "In a galaxy far away,", "The quick brown fox,", "To be or not to be,",
    "There was light,", "It was a cold day,", "A village in the mountains,", "Beneath the deep ocean,",
    "At midnight, a figure appeared,", "A hidden forest path,", "Where dragons still roam,", 
    "The wind howled outside,", "Two travelers shared tales,", "A secret meeting began,", 
    "A treasure map in hand,", "Snow fell softly,", "The sky turned red,", "A shadow moved quickly,", 
    "On the edge of space,", "The ship set sail,", "Whispers filled the air,", "The child looked up,", 
    "Stars twinkled above,", "A fire burned bright,", "The knight drew his sword,", "A door creaked open,", 
    "The castle stood empty,", "The wolves began to howl,", "A key was found,", "The storm raged on,"
]

rate_schedule = [
    # {"duration": 60, "requests_per_minute_A": 50, "requests_per_minute_B": 0},
    # {"duration": 60, "requests_per_minute_A": 25, "requests_per_minute_B": 10},
    {"duration": 60, "requests_per_minute_A": 25, "requests_per_minute_B": 0},
    # {"duration": 60, "requests_per_minute_A": 50, "requests_per_minute_B": 20},
    # {"duration": 60, "requests_per_minute_A": 40, "requests_per_minute_B": 20},
]

def send_request(url, headers, data, interval, results, job_name):
    """
    Sends HTTP requests at a given interval and records the response times,
    along with the prompt and response content.
    """
    if interval == float("inf"):
        print(f"Skipping job {job_name} as the interval is infinite.")
        return

    while not stop_event.is_set():
        data["prompt"] = random.choice(prompts)
        start_time = time.perf_counter()  # Use performance counter for precise durations
        prompt = data["prompt"]
        try:
            response = requests.post(url, headers=headers, json=data)
            response_text = response.text
            response_text = json.loads(response_text)
            response_text = response_text["choices"][0]["text"]
            response_text = response_text.replace("\n", " ")
            elapsed_time = time.perf_counter() - start_time  # Calculate request duration
            results.append((elapsed_time, prompt, response_text))
        except Exception as e:
            print(f"Request failed in {job_name}: {e}")
            elapsed_time = time.perf_counter() - start_time
            results.append((elapsed_time, prompt, "ERROR"))

        # Sleep for the remaining time in the interval
        remaining_time = interval - elapsed_time
        if remaining_time > 0:
            time.sleep(remaining_time)


def dynamic_job_manager(jobs, headers, data, job_results):
    threads = []
    for phase in rate_schedule:
        print(f"Starting phase with duration {phase['duration']} seconds")
        
        # Update jobs dynamically
        for job in jobs:
            if job["job_name"] == "LLM #A":
                job["interval"] = 1 / phase["requests_per_minute_A"] * 60 if phase["requests_per_minute_A"] > 0 else float("inf")
            elif job["job_name"] == "LLM #B":
                job["interval"] = 1 / phase["requests_per_minute_B"] * 60 if phase["requests_per_minute_B"] > 0 else float("inf")
        
        # Launch threads for this phase
        threads = [
            Thread(target=send_request, args=(job["url"], headers, data, job["interval"], job_results[job["job_name"]], job["job_name"]))
            for job in jobs
        ]
        for thread in threads:
            thread.start()
            time.sleep(0.5)
        
        time.sleep(phase["duration"])
        
        # Stop all threads
        stop_event.set()
        for thread in threads:
            thread.join()
        stop_event.clear()

        # Calculate and print throughput and average request time
        total_requests = 0
        for job_name, results in job_results.items():
            num_requests = len(results)
            total_requests += num_requests
            if num_requests > 0:
                avg_request_time = sum(result[0] for result in results) / num_requests
            else:
                avg_request_time = 0
            print(f"[{job_name}] Throughput: {num_requests / phase['duration'] * 60:.2f} requests per minute")
            print(f"[{job_name}] Average Request Time: {avg_request_time:.2f} seconds")
        
        print(f"Phase complete. Total throughput: {total_requests / phase['duration'] * 60:.2f} requests per minute")

        # Clear results for the next phase
        for results in job_results.values():
            results.clear()

        print("\n")
        time.sleep(5)

    print("All phases completed.")

def main():
    headers = {
        "accept": "application/json",
        "Content-Type": "application/json"
    }
    data = {
        "model": "meta/llama-3.1-8b-instruct",
        "max_tokens": 80
    }

    jobs = [
        {"url": "http://localhost:8001/v1/completions", "interval": 0, "job_name": "LLM #A"},
        {"url": "http://localhost:8002/v1/completions", "interval": 0, "job_name": "LLM #B"},
    ]

    # Initialize job results
    for job_data in jobs:
        job_results[job_data["job_name"]] = []

    # Start the dynamic job manager
    dynamic_job_manager(jobs, headers, data, job_results)

if __name__ == "__main__":
    main()
