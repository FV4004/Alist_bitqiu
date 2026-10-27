import json, os, urllib.request, urllib.error

def request(url, method="GET", data=None, token=None):
    headers={"Content-Type":"application/json"}
    if token: headers["Authorization"]="Bearer "+token
    req=urllib.request.Request(url,data=json.dumps(data).encode() if data is not None else None,headers=headers,method=method)
    try:
        with urllib.request.urlopen(req,timeout=60) as r:
            raw=r.read();return json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        if method=="DELETE" and e.code==404:return None
        raise RuntimeError("Registry operation failed: HTTP "+str(e.code)) from None

user=os.environ["DOCKERHUB_USERNAME"]
login=request("https://hub.docker.com/v2/users/login/","POST",{"username":user,"password":os.environ["DOCKERHUB_TOKEN"]})
for tag in ["sha-327411b3acf4","sha-854527d0c2dd"]:
    request("https://hub.docker.com/v2/repositories/"+user+"/alist_bitqiu/tags/"+tag+"/","DELETE",token=login["token"])
    print("Docker Hub tag withdrawn:",tag)
# Delete only versions carrying withdrawn tags, never the restored latest.
base="https://api.github.com/users/FV4004/packages/container/alist_bitqiu/versions"
versions=request(base+"?per_page=100",token=os.environ["GH_TOKEN"])
for v in versions:
    tags=v.get("metadata",{}).get("container",{}).get("tags",[])
    if set(tags)&{"sha-327411b3acf4","sha-854527d0c2dd"}:
        if "latest" in tags:raise RuntimeError("Refusing deletion: version still carries latest")
        request(base+"/"+str(v["id"]),"DELETE",token=os.environ["GH_TOKEN"])
        print("GHCR withdrawn version deleted:",v["id"])
