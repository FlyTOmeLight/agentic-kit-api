#!/usr/bin/env python3
"""Publish an Agent definition through the same API for every application."""
import argparse,json,os,urllib.request

def main():
 parser=argparse.ArgumentParser(description=__doc__)
 parser.add_argument('definition',help='JSON CreateAgentRequest file')
 parser.add_argument('--agent-id',help='Update this existing Agent, preserving its identity')
 args=parser.parse_args()
 base=os.environ['AGENTIC_URL'].rstrip('/')
 token=os.environ['AGENTIC_TOKEN']
 def request(method,path,body=None):
  req=urllib.request.Request(base+path,data=json.dumps(body,ensure_ascii=False).encode() if body is not None else None,method=method,headers={'Content-Type':'application/json','Authorization':'Bearer '+token})
  with urllib.request.urlopen(req,timeout=30) as response:return json.load(response)
 with open(args.definition) as file:definition=json.load(file)
 if args.agent_id:
  from urllib.parse import quote
  path='/v1/agents/'+quote(args.agent_id,safe='')
  current=request('GET',path)
  agent=request('PUT',path,{**definition,'revision':current['revision']})
 else:
  agent=request('POST','/v1/agents',definition)
 version=request('POST','/v1/agents/'+agent['id']+'/publish',{'revision':agent['revision']})
 print(json.dumps({'agentID':agent['id'],'agentVersion':version['number'],'name':agent['name']},ensure_ascii=False))
if __name__=='__main__':main()
