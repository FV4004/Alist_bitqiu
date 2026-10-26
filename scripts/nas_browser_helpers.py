"""Keep legacy picker/download fixtures independent of the new default size limit.

Use the public clear control on each NAS document. The dedicated size-filter
acceptance suite deliberately does not install this initializer.
"""
UNRESTRICTED_SIZE = """
document.addEventListener('DOMContentLoaded', () => {
  document.getElementById('size-clear')?.click();
}, {once: true});
"""
