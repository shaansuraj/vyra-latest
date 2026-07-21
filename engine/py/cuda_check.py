# cuda_check.py
import sys
import time

try:
    import torch
except ImportError:
    print("PyTorch is not installed. Install with: pip install torch")
    sys.exit(1)

def bytes_to_gb(x: int) -> float:
    return x / (1024 ** 3)

print("PyTorch version:", torch.__version__)
print("CUDA available:", torch.cuda.is_available())
print("CUDA version (torch build):", torch.version.cuda)
print("cuDNN available:", torch.backends.cudnn.is_available())
print("cuDNN version:", torch.backends.cudnn.version())

if not torch.cuda.is_available():
    print("\nNo CUDA devices detected by PyTorch.")
    sys.exit(0)

count = torch.cuda.device_count()
print("\nCUDA device count:", count)

for i in range(count):
    props = torch.cuda.get_device_properties(i)
    print(f"\n[{i}] {torch.cuda.get_device_name(i)}")
    print(f"    Compute capability: {props.major}.{props.minor}")
    print(f"    Total memory: {bytes_to_gb(props.total_memory):.2f} GB")
    print(f"    Multi-processor count: {props.multi_processor_count}")

print("\n================ GPU ACCELERATOR SMOKE TEST ================")

def gpu_smoke_test(device_idx: int = 0, n: int = 4096):
    device = torch.device(f"cuda:{device_idx}")

    # 1) Quick allocation + basic op
    try:
        torch.cuda.set_device(device_idx)
        torch.cuda.empty_cache()
        torch.cuda.synchronize()
    except Exception as e:
        return False, f"Failed to initialize CUDA device {device_idx}: {e}"

    # 2) Matrix multiply test (heavy enough to confirm compute works)
    try:
        a = torch.randn((n, n), device=device, dtype=torch.float16)
        b = torch.randn((n, n), device=device, dtype=torch.float16)

        # Warmup
        _ = a @ b
        torch.cuda.synchronize()

        start = time.time()
        c = a @ b
        torch.cuda.synchronize()
        elapsed = time.time() - start

        # 3) Correctness sanity check on smaller slice (avoid full fp16 error explosion)
        # Compare a small block in fp32 on CPU for a rough validation
        a_small = a[:256, :256].float().cpu()
        b_small = b[:256, :256].float().cpu()
        c_expected = a_small @ b_small

        c_small = c[:256, :256].float().cpu()
        max_abs_err = (c_small - c_expected).abs().max().item()

        # 4) Memory check (allocated)
        allocated = torch.cuda.memory_allocated(device_idx)
        reserved = torch.cuda.memory_reserved(device_idx)

        return True, {
            "device": str(device),
            "matmul_size": f"{n}x{n}",
            "time_sec": elapsed,
            "max_abs_err_block256": max_abs_err,
            "mem_allocated_gb": bytes_to_gb(allocated),
            "mem_reserved_gb": bytes_to_gb(reserved),
        }

    except RuntimeError as e:
        return False, f"CUDA RuntimeError during compute: {e}"
    except Exception as e:
        return False, f"Unexpected error during compute: {e}"

ok, info = gpu_smoke_test(device_idx=0, n=4096)

if not ok:
    print("❌ GPU smoke test FAILED")
    print("Reason:", info)
    sys.exit(2)

print("✅ GPU smoke test PASSED")
print("Details:")
for k, v in info.items():
    print(f"  - {k}: {v}")

print("\nExtra quick check (GPU vs CPU add):")
x = torch.ones(10, device="cuda")
y = torch.ones(10, device="cuda")
z = (x + y).cpu().tolist()
print("  Result:", z)
